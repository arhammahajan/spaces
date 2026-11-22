// Read-only router HTTP queries: status, model listing, config validation.
package routerctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Status is the point-in-time router overview. Degradation is expressed in
// the fields, never as an error: Livez/Ready flip on probe outcome, and a
// 403 /diagnostics becomes DiagnosticsNote instead of a failure.
type Status struct {
	Managed         string          // "systemd", "direct", "none"
	URL             string          //
	Livez           bool            //
	Ready           bool            //
	Version         string          // from GET / ("name"+"version" fields)
	Models          int             // count from GET /v1/models
	ModelIDs        []string        // first 200 ids, sorted
	Diagnostics     json.RawMessage // raw /diagnostics body; nil when unavailable
	DiagnosticsNote string          // "" or e.g. "403 diagnostics_disabled"
}

// ModelInfo is one entry of the GET /v1/models listing.
type ModelInfo struct {
	ID      string
	OwnedBy string
	Router  bool // true when metadata.router is present as an object
}

func httpClient(opts Options) *http.Client {
	return &http.Client{Timeout: opts.httpTimeout()}
}

func baseURL(opts Options) string {
	return strings.TrimRight(strings.TrimSpace(opts.Router.URL), "/")
}

func authHeader(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

func doGet(ctx context.Context, client *http.Client, url, key string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	authHeader(req, key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// managedMode reports "systemd" when the user unit exists, "direct" when
// the pidfile names a live process, and "none" otherwise.
func managedMode(ctx context.Context, opts Options) string {
	if systemdManaged(ctx, opts) {
		return "systemd"
	}
	if pid, err := readPidFile(opts.PidPath); err == nil && pidAlive(opts.killFn(), pid) {
		return "direct"
	}
	return "none"
}

// FetchStatus probes the router: GET / for name+version, GET /livez,
// GET /readyz (200 vs 503), GET /v1/models (count + first 200 sorted ids),
// and GET /diagnostics with graceful degradation. Individual probe
// failures degrade the result; only a malformed URL is an error.
func FetchStatus(ctx context.Context, opts Options) (Status, error) {
	base := baseURL(opts)
	if base == "" {
		return Status{}, fmt.Errorf("no router URL configured")
	}
	if _, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil); err != nil {
		return Status{}, fmt.Errorf("invalid router URL %q: %w", opts.Router.URL, err)
	}
	st := Status{Managed: managedMode(ctx, opts), URL: base}
	client := httpClient(opts)
	key := opts.Router.APIKey

	if code, body, err := doGet(ctx, client, base+"/", ""); err == nil && code == 200 {
		var index struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &index) == nil {
			st.Version = index.Version
		}
	}
	if code, _, err := doGet(ctx, client, base+"/livez", ""); err == nil && code == 200 {
		st.Livez = true
	}
	if code, _, err := doGet(ctx, client, base+"/readyz", ""); err == nil && code == 200 {
		st.Ready = true
	}
	if models, err := ListModels(ctx, opts); err == nil {
		st.Models = len(models)
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		sort.Strings(ids)
		if len(ids) > 200 {
			ids = ids[:200]
		}
		st.ModelIDs = ids
	}
	switch code, body, err := doGet(ctx, client, base+"/diagnostics", key); {
	case err != nil:
		st.DiagnosticsNote = "diagnostics_unavailable"
	case code == 200:
		st.Diagnostics = append(json.RawMessage(nil), body...)
	case code == 403:
		st.DiagnosticsNote = "403 diagnostics_disabled"
	case code == 401:
		st.DiagnosticsNote = "401 diagnostics_unauthorized"
	default:
		st.DiagnosticsNote = fmt.Sprintf("diagnostics_status_%d", code)
	}
	return st, nil
}

// ListModels returns the GET /v1/models listing, sending
// Authorization: Bearer <key> when the key is non-empty. It tolerates both
// {"data": [...]} and bare [...] bodies. Router is true when the entry's
// metadata is an object containing a "router" key.
func ListModels(ctx context.Context, opts Options) ([]ModelInfo, error) {
	base := baseURL(opts)
	if base == "" {
		return nil, fmt.Errorf("no router URL configured")
	}
	code, body, err := doGet(ctx, httpClient(opts), base+"/v1/models", opts.Router.APIKey)
	if err != nil {
		return nil, fmt.Errorf("get %s/v1/models: %w", base, err)
	}
	if code != 200 {
		return nil, fmt.Errorf("get %s/v1/models: status %d", base, code)
	}
	return parseModels(body)
}

func parseModels(body []byte) ([]ModelInfo, error) {
	var raw json.RawMessage = body
	// Tolerate {"data": [...]} and bare [...] arrays.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
		raw = envelope.Data
	}
	var items []struct {
		ID       string          `json:"id"`
		OwnedBy  string          `json:"owned_by"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse models listing: %w", err)
	}
	out := make([]ModelInfo, 0, len(items))
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		out = append(out, ModelInfo{ID: it.ID, OwnedBy: it.OwnedBy, Router: hasRouterFact(it.Metadata)})
	}
	return out, nil
}

func hasRouterFact(meta json.RawMessage) bool {
	if len(meta) == 0 {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(meta, &obj); err != nil {
		return false
	}
	_, ok := obj["router"]
	return ok
}

// ValidateConfigWithOptions validates the router config at path offline.
// It always verifies that path is readable valid TOML, and when opts.RouterBin
// resolves to an executable binary it runs `<RouterBin> --validate-config --config <path>`
// for full semantic validation.
func ValidateConfigWithOptions(ctx context.Context, opts Options, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if bin := resolveRouterBin(opts); bin != "" {
		cmd := exec.CommandContext(ctx, bin, "--validate-config", "--config", path)
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(buf.String())
			if msg != "" {
				return fmt.Errorf("validate config %s: %s", path, msg)
			}
			return fmt.Errorf("validate config %s: %w", path, err)
		}
	}
	return nil
}

func resolveRouterBin(opts Options) string {
	bin := strings.TrimSpace(opts.RouterBin)
	if bin == "" {
		return ""
	}
	if strings.ContainsRune(bin, os.PathSeparator) {
		if fileExists(bin) {
			return bin
		}
		return ""
	}
	if resolved, err := opts.lookPathFn()(bin); err == nil && resolved != "" {
		return resolved
	}
	return ""
}
