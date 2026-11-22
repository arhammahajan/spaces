package gitinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arhammahajan/spaces/internal/model"
)

const (
	ghCmdTimeout      = 6 * time.Second
	maxBatchBranches  = 32
	prListLimit       = "100"
	prListFieldsCheck = "number,title,state,isDraft,headRefName,url,statusCheckRollup"
	prListFieldsBasic = "number,title,state,isDraft,headRefName,url"
)

// BranchGitHub holds the GitHub PR and CI check summary resolved for one branch.
type BranchGitHub struct {
	PR     *model.PRSummary
	Checks *model.CheckSummary
}

type repoIdentity struct {
	owner    string
	name     string
	hostname string
}

// CheckRollupItem handles both GitHub CheckRun (status/conclusion) and
// StatusContext (state) entries from GraphQL or `gh pr list --json`.
type CheckRollupItem struct {
	Name       string `json:"name,omitempty"`
	Context    string `json:"context,omitempty"`
	Status     string `json:"status,omitempty"`
	State      string `json:"state,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

// AggregateChecks folds raw GitHub check runs and status contexts into a single
// CheckSummary, mirroring workmux's aggregation rules.
func AggregateChecks(items []CheckRollupItem) *model.CheckSummary {
	if len(items) == 0 {
		return nil
	}
	var passed, failed, pending, skipped int
	var failingName string

	for _, item := range items {
		status := strings.ToUpper(strings.TrimSpace(item.Status))
		if status == "" {
			status = strings.ToUpper(strings.TrimSpace(item.State))
		}
		conclusion := strings.ToUpper(strings.TrimSpace(item.Conclusion))
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.Context)
		}

		switch {
		case conclusion == "SUCCESS" || (conclusion == "" && status == "SUCCESS"):
			passed++
		case conclusion == "FAILURE" || conclusion == "CANCELLED" || conclusion == "TIMED_OUT" ||
			conclusion == "STARTUP_FAILURE" || conclusion == "ACTION_REQUIRED" ||
			(conclusion == "" && (status == "FAILURE" || status == "ERROR")):
			failed++
			if failingName == "" && name != "" {
				failingName = name
			}
		case conclusion == "NEUTRAL" || conclusion == "SKIPPED":
			skipped++
		case status == "IN_PROGRESS" || status == "QUEUED" || status == "PENDING" ||
			status == "REQUESTED" || status == "WAITING":
			pending++
		}
	}

	total := passed + failed + pending
	if total == 0 {
		if skipped > 0 {
			return &model.CheckSummary{State: model.CheckSuccess}
		}
		return nil
	}
	if failed > 0 {
		return &model.CheckSummary{
			State:       model.CheckFailure,
			Passed:      passed,
			Total:       total,
			FailingName: failingName,
		}
	}
	if pending > 0 {
		return &model.CheckSummary{
			State:  model.CheckPending,
			Passed: passed,
			Total:  total,
		}
	}
	return &model.CheckSummary{
		State:  model.CheckSuccess,
		Passed: passed,
		Total:  total,
	}
}

func resolveRepoIdentity(ctx context.Context, repoRoot string, run CommandRunner) (repoIdentity, error) {
	if run == nil {
		run = defaultRunCommand
	}
	gctx, cancel := context.WithTimeout(ctx, ghCmdTimeout)
	defer cancel()

	out, code, err := run(gctx, repoRoot, nil, "gh", "repo", "view", "--json", "owner,name,url")
	if err != nil || code != 0 {
		return repoIdentity{}, fmt.Errorf("gh repo view failed: %w", err)
	}
	var raw struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return repoIdentity{}, err
	}
	if raw.Owner.Login == "" || raw.Name == "" {
		return repoIdentity{}, fmt.Errorf("gh repo view returned empty owner/name")
	}
	hostname := "github.com"
	u := raw.URL
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if host, _, ok := strings.Cut(u, "/"); ok && host != "" {
		hostname = host
	}
	return repoIdentity{
		owner:    raw.Owner.Login,
		name:     raw.Name,
		hostname: hostname,
	}, nil
}

// FetchRepoGitHub queries GitHub PR and CI check summaries for the given
// branches of repoRoot using a single batched GraphQL query (with fallback to
// `gh pr list`).
func FetchRepoGitHub(ctx context.Context, repoRoot string, id repoIdentity, branches []string, run CommandRunner) (map[string]BranchGitHub, error) {
	if len(branches) == 0 {
		return map[string]BranchGitHub{}, nil
	}
	if run == nil {
		run = defaultRunCommand
	}

	if res, err := fetchBranchesGraphQL(ctx, id, branches, run); err == nil {
		return res, nil
	}
	return fetchBranchesREST(ctx, repoRoot, branches, run)
}

func fetchBranchesGraphQL(ctx context.Context, id repoIdentity, branches []string, run CommandRunner) (map[string]BranchGitHub, error) {
	result := make(map[string]BranchGitHub, len(branches))
	for start := 0; start < len(branches); start += maxBatchBranches {
		end := start + maxBatchBranches
		if end > len(branches) {
			end = len(branches)
		}
		chunk := branches[start:end]
		part, err := fetchBranchesGraphQLChunk(ctx, id, chunk, run)
		if err != nil {
			return nil, err
		}
		for k, v := range part {
			result[k] = v
		}
	}
	return result, nil
}

func fetchBranchesGraphQLChunk(ctx context.Context, id repoIdentity, branches []string, run CommandRunner) (map[string]BranchGitHub, error) {
	decls := []string{"$owner: String!", "$name: String!"}
	vars := map[string]any{
		"owner": id.owner,
		"name":  id.name,
	}
	var fields []string
	for i, b := range branches {
		headVar := fmt.Sprintf("head_%d", i)
		refVar := fmt.Sprintf("ref_%d", i)
		decls = append(decls, "$"+headVar+": String!", "$"+refVar+": String!")
		vars[headVar] = b
		vars[refVar] = "refs/heads/" + b
		fields = append(fields, fmt.Sprintf(`    pr_%d: pullRequests(headRefName: $%s, first: 1, states: [OPEN, MERGED, CLOSED], orderBy: {field: CREATED_AT, direction: DESC}) {
      nodes {
        number title state isDraft url
        commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) {
          nodes { __typename ... on CheckRun { name status conclusion } ... on StatusContext { context state } }
        } } } } }
      }
    }
    ref_%d: ref(qualifiedName: $%s) {
      target { ... on Commit { statusCheckRollup { contexts(first: 100) {
        nodes { __typename ... on CheckRun { name status conclusion } ... on StatusContext { context state } }
      } } } }
    }`, i, headVar, i, refVar))
	}

	query := fmt.Sprintf("query(%s) {\n  repository(owner: $owner, name: $name) {\n%s\n  }\n}",
		strings.Join(decls, ", "), strings.Join(fields, "\n"))

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": vars,
	})
	if err != nil {
		return nil, err
	}

	gctx, cancel := context.WithTimeout(ctx, ghCmdTimeout)
	defer cancel()

	out, code, err := run(gctx, "", body, "gh", "api", "graphql", "--hostname", id.hostname, "--input", "-")
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository map[string]json.RawMessage `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if jerr := json.Unmarshal(out, &resp); jerr != nil {
		return nil, jerr
	}
	if resp.Data.Repository == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("graphql: %s", resp.Errors[0].Message)
		}
		if code != 0 {
			return nil, fmt.Errorf("gh api graphql exited %d", code)
		}
		return map[string]BranchGitHub{}, nil
	}

	outMap := make(map[string]BranchGitHub, len(branches))
	for i, b := range branches {
		prKey := fmt.Sprintf("pr_%d", i)
		refKey := fmt.Sprintf("ref_%d", i)
		summary := parseGraphQLBranch(resp.Data.Repository[prKey], resp.Data.Repository[refKey])
		outMap[b] = summary
	}
	return outMap, nil
}

type gqlRollup struct {
	Contexts struct {
		Nodes []CheckRollupItem `json:"nodes"`
	} `json:"contexts"`
}

func parseGraphQLBranch(prRaw, refRaw json.RawMessage) BranchGitHub {
	var res BranchGitHub
	if len(prRaw) > 0 && string(prRaw) != "null" {
		var prConn struct {
			Nodes []struct {
				Number  int    `json:"number"`
				Title   string `json:"title"`
				State   string `json:"state"`
				IsDraft bool   `json:"isDraft"`
				URL     string `json:"url"`
				Commits struct {
					Nodes []struct {
						Commit struct {
							StatusCheckRollup *gqlRollup `json:"statusCheckRollup"`
						} `json:"commit"`
					} `json:"nodes"`
				} `json:"commits"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(prRaw, &prConn); err == nil && len(prConn.Nodes) > 0 {
			n := prConn.Nodes[0]
			res.PR = &model.PRSummary{
				Number:  n.Number,
				Title:   n.Title,
				State:   n.State,
				IsDraft: n.IsDraft,
				URL:     n.URL,
			}
			if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
				res.Checks = AggregateChecks(n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes)
			}
		}
	}
	if res.Checks == nil && len(refRaw) > 0 && string(refRaw) != "null" {
		var refObj struct {
			Target *struct {
				StatusCheckRollup *gqlRollup `json:"statusCheckRollup"`
			} `json:"target"`
		}
		if err := json.Unmarshal(refRaw, &refObj); err == nil && refObj.Target != nil && refObj.Target.StatusCheckRollup != nil {
			res.Checks = AggregateChecks(refObj.Target.StatusCheckRollup.Contexts.Nodes)
		}
	}
	return res
}

func fetchBranchesREST(ctx context.Context, repoRoot string, branches []string, run CommandRunner) (map[string]BranchGitHub, error) {
	want := make(map[string]bool, len(branches))
	for _, b := range branches {
		want[b] = true
	}

	gctx, cancel := context.WithTimeout(ctx, ghCmdTimeout)
	defer cancel()

	out, code, err := run(gctx, repoRoot, nil, "gh", "pr", "list", "--state", "all", "--limit", prListLimit, "--json", prListFieldsCheck)
	if err != nil || code != 0 {
		out, code, err = run(gctx, repoRoot, nil, "gh", "pr", "list", "--state", "all", "--limit", prListLimit, "--json", prListFieldsBasic)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("gh pr list failed: %w", err)
		}
	}

	var prs []struct {
		Number      int               `json:"number"`
		Title       string            `json:"title"`
		State       string            `json:"state"`
		IsDraft     bool              `json:"isDraft"`
		HeadRefName string            `json:"headRefName"`
		URL         string            `json:"url"`
		Checks      []CheckRollupItem `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, err
	}

	result := make(map[string]BranchGitHub, len(branches))
	for _, pr := range prs {
		if !want[pr.HeadRefName] {
			continue
		}
		// gh pr list returns newest first; keep the first match per branch.
		if _, exists := result[pr.HeadRefName]; exists {
			continue
		}
		result[pr.HeadRefName] = BranchGitHub{
			PR: &model.PRSummary{
				Number:  pr.Number,
				Title:   pr.Title,
				State:   pr.State,
				IsDraft: pr.IsDraft,
				URL:     pr.URL,
			},
			Checks: AggregateChecks(pr.Checks),
		}
	}
	return result, nil
}
