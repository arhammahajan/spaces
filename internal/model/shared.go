package model

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SharedKind filters items in the Shared MCP & Skills popup.
type SharedKind int

const (
	SharedFilterAll SharedKind = iota
	SharedFilterMCP
	SharedFilterSkills
)

// Label returns the short pill label for a shared filter.
func (k SharedKind) Label() string {
	switch k {
	case SharedFilterMCP:
		return "mcp"
	case SharedFilterSkills:
		return "skills"
	default:
		return "all"
	}
}

// AllSharedFilters returns all shared filter tabs in display order.
func AllSharedFilters() []SharedKind {
	return []SharedKind{SharedFilterAll, SharedFilterMCP, SharedFilterSkills}
}

// SharedSkill represents one skill discovered under <shared>/skills/<dir>/SKILL.md.
type SharedSkill struct {
	Name                   string
	DirName                string
	Description            string
	Path                   string
	DirPath                string
	DisableModelInvocation bool
	Paths                  []string
	Icon                   string
	Color                  string
	ExtraFiles             int
	HasReferences          bool
	HasScripts             bool
	Body                   string
	ModTime                time.Time
}

// SharedMCP represents one shared MCP server entry discovered in <shared>
// (or aggregated from sibling harness configs when <shared> has no standalone
// MCP definitions).
type SharedMCP struct {
	Name        string
	Transport   string
	Command     string
	Args        []string
	URL         string
	Description string
	Exposure    string
	Disabled    bool
	Env         map[string]string
	SourcePath  string
	SourceKind  string // "shared" or "harnesses"
	Harnesses   []string
	RawJSON     string
}

// CommandLine returns a single-line human-readable command or URL representation.
func (m SharedMCP) CommandLine() string {
	if cmd := strings.TrimSpace(m.Command); cmd != "" {
		if len(m.Args) == 0 {
			return cmd
		}
		return cmd + " " + strings.Join(m.Args, " ")
	}
	return strings.TrimSpace(m.URL)
}

// SharedSnapshot holds all shared MCP servers and skills discovered under RootDir.
type SharedSnapshot struct {
	RootDir    string
	SkillsDir  string
	MCPs       []SharedMCP
	Skills     []SharedSkill
	LoadedAt   time.Time
	RootExists bool
}

// DefaultSharedDir resolves the shared skills/MCP root directory, defaulting
// to the Stow-resolved ~/.agents directory in $HOME (falling back to the
// legacy ~/dev/dots/harnesses/shared layout when ~/.agents does not exist).
// Overridable via explicit dir or TMUX_SPACES_SHARED_DIR. If HOME points into
// a shadow profile under ~/.harness/ and the candidate does not exist
// there, it falls back to the real home prefix before /.harness/.
func DefaultSharedDir(explicit, homeDir string) string {
	raw := strings.TrimSpace(explicit)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("TMUX_SPACES_SHARED_DIR"))
	}
	if raw != "" {
		return expandHomePath(raw, homeDir)
	}

	homes := candidateHomes(homeDir)
	for _, h := range homes {
		if cand := filepath.Join(h, ".agents"); isDir(cand) {
			return cand
		}
	}
	for _, h := range homes {
		if cand := filepath.Join(h, "dev", "dots", "harnesses", "shared"); isDir(cand) {
			return cand
		}
	}
	return filepath.Join(realHomeDir(homeDir), ".agents")
}

func realHomeDir(homeDir string) string {
	if idx := strings.Index(homeDir, string(filepath.Separator)+".harness"+string(filepath.Separator)); idx > 0 {
		return homeDir[:idx]
	}
	return homeDir
}

func candidateHomes(homeDir string) []string {
	if homeDir == "" {
		return nil
	}
	real := realHomeDir(homeDir)
	if real != homeDir {
		return []string{homeDir, real}
	}
	return []string{homeDir}
}

func expandHomePath(p, homeDir string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if homeDir != "" {
		if p == "~" {
			return filepath.Clean(homeDir)
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Clean(filepath.Join(homeDir, p[2:]))
		}
	}
	return filepath.Clean(p)
}

func isDir(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// LoadShared scans dir (or DefaultSharedDir when empty) for shared skills and
// MCP server definitions.
func LoadShared(dir, homeDir string) (SharedSnapshot, error) {
	root := DefaultSharedDir(dir, homeDir)
	snap := SharedSnapshot{
		RootDir:    root,
		SkillsDir:  filepath.Join(root, "skills"),
		LoadedAt:   time.Now(),
		RootExists: isDir(root),
	}
	if !snap.RootExists {
		return snap, nil
	}

	snap.Skills = loadSharedSkills(snap.SkillsDir)
	snap.MCPs = loadSharedMCPs(root, homeDir)
	return snap, nil
}

// ---------------------------------------------------------------------------
// Skills loader
// ---------------------------------------------------------------------------

func loadSharedSkills(skillsDir string) []SharedSkill {
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil
	}
	skills := make([]SharedSkill, 0, len(entries))
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirPath := filepath.Join(skillsDir, name)
		if !isDir(dirPath) {
			continue
		}
		skillPath := findSkillDoc(dirPath)
		if skillPath == "" {
			continue
		}
		sk, ok := parseSkillFile(name, dirPath, skillPath)
		if !ok {
			continue
		}
		skills = append(skills, sk)
	}
	sort.Slice(skills, func(i, j int) bool {
		li, lj := strings.ToLower(skills[i].Name), strings.ToLower(skills[j].Name)
		if li != lj {
			return li < lj
		}
		return skills[i].DirName < skills[j].DirName
	})
	return skills
}

func findSkillDoc(dirPath string) string {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(dirPath, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func parseSkillFile(dirName, dirPath, skillPath string) (SharedSkill, bool) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return SharedSkill{}, false
	}
	fi, _ := os.Stat(skillPath)
	var modTime time.Time
	if fi != nil {
		modTime = fi.ModTime()
	}

	fm, body := splitFrontmatter(string(data))
	sk := SharedSkill{
		Name:                   dirName,
		DirName:                dirName,
		Path:                   skillPath,
		DirPath:                dirPath,
		Body:                   strings.TrimSpace(body),
		ModTime:                modTime,
		DisableModelInvocation: fm.disableModelInvocation,
		Paths:                  fm.paths,
		Icon:                   fm.icon,
		Color:                  fm.color,
	}
	if fm.name != "" {
		sk.Name = fm.name
	}
	if fm.description != "" {
		sk.Description = normalizeInline(fm.description)
	} else {
		sk.Description = firstMarkdownParagraph(sk.Body)
	}
	sk.ExtraFiles, sk.HasReferences, sk.HasScripts = inspectSkillDir(dirPath, filepath.Base(skillPath))
	return sk, true
}

func inspectSkillDir(dirPath, mainDoc string) (extra int, hasRefs, hasScripts bool) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0, false, false
	}
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, ".") || name == mainDoc {
			continue
		}
		if de.IsDir() {
			lower := strings.ToLower(name)
			if lower == "references" || lower == "reference" {
				hasRefs = true
			} else if lower == "scripts" || lower == "script" {
				hasScripts = true
			}
			_ = filepath.WalkDir(filepath.Join(dirPath, name), func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
					extra++
				}
				return nil
			})
			continue
		}
		extra++
	}
	return extra, hasRefs, hasScripts
}

type skillFrontmatter struct {
	name                   string
	description            string
	disableModelInvocation bool
	paths                  []string
	icon                   string
	color                  string
}

func splitFrontmatter(raw string) (skillFrontmatter, string) {
	var fm skillFrontmatter
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimPrefix(raw, "\uFEFF")
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return fm, raw
	}
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" || t == "..." {
			endIdx = i
			break
		}
	}
	if endIdx < 0 {
		return fm, raw
	}
	fmLines := lines[1:endIdx]
	body := strings.Join(lines[endIdx+1:], "\n")

	for i := 0; i < len(fmLines); i++ {
		ln := fmLines[i]
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Top-level YAML key starts at column 0.
		if ln[0] == ' ' || ln[0] == '\t' {
			continue
		}
		colon := strings.IndexByte(ln, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(ln[:colon])
		val := strings.TrimSpace(ln[colon+1:])

		switch key {
		case "name":
			fm.name = parseYAMLScalar(val)
		case "icon":
			fm.icon = parseYAMLScalar(val)
		case "color":
			fm.color = parseYAMLScalar(val)
		case "disable-model-invocation":
			fm.disableModelInvocation = strings.EqualFold(parseYAMLScalar(val), "true")
		case "description":
			if val == ">" || val == ">-" || val == "|" || val == "|-" {
				var block []string
				for i+1 < len(fmLines) {
					next := fmLines[i+1]
					if next == "" {
						block = append(block, "")
						i++
						continue
					}
					if next[0] == ' ' || next[0] == '\t' {
						block = append(block, strings.TrimSpace(next))
						i++
						continue
					}
					break
				}
				fm.description = strings.Join(block, " ")
			} else {
				fm.description = parseYAMLScalar(val)
			}
		case "paths":
			if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
				fm.paths = parseYAMLInlineList(val)
			} else if val == "" {
				for i+1 < len(fmLines) {
					next := strings.TrimSpace(fmLines[i+1])
					if strings.HasPrefix(next, "- ") {
						item := parseYAMLScalar(strings.TrimSpace(next[2:]))
						if item != "" {
							fm.paths = append(fm.paths, item)
						}
						i++
						continue
					}
					break
				}
			} else {
				if item := parseYAMLScalar(val); item != "" {
					fm.paths = append(fm.paths, item)
				}
			}
		}
	}
	return fm, body
}

func parseYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var unq string
		if err := json.Unmarshal([]byte(s), &unq); err == nil {
			return unq
		}
		return strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

func parseYAMLInlineList(s string) []string {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]"))
	if inner == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && inDouble {
			cur.WriteByte(c)
			escaped = true
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			cur.WriteByte(c)
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			cur.WriteByte(c)
			continue
		}
		if c == ',' && !inSingle && !inDouble {
			if item := parseYAMLScalar(cur.String()); item != "" {
				out = append(out, item)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if item := parseYAMLScalar(cur.String()); item != "" {
		out = append(out, item)
	}
	return out
}

func normalizeInline(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func firstMarkdownParagraph(body string) string {
	var para []string
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, t)
	}
	return normalizeInline(strings.Join(para, " "))
}

// ---------------------------------------------------------------------------
// MCP loader
// ---------------------------------------------------------------------------

func loadSharedMCPs(root, homeDir string) []SharedMCP {
	// Pass 1: discover MCP configs directly under <root> (e.g. mcp.json,
	// mcp_config.json, mcp/*.json, mcps/*.json, *mcp*.toml).
	direct := loadDirectMCPs(root)
	if len(direct) > 0 {
		return direct
	}

	// Pass 2: if <root> is a "shared" directory with no standalone MCP config
	// yet, aggregate shared MCP definitions tracked across sibling harness
	// directories under filepath.Dir(root).
	if filepath.Base(root) == "shared" {
		return loadSiblingHarnessMCPs(filepath.Dir(root))
	}

	// Pass 3: when <root> is the Stow-resolved ~/.agents directory (or $HOME),
	// aggregate MCP definitions across stowed harness configs in $HOME.
	if filepath.Base(root) == ".agents" {
		return loadHomeHarnessMCPs(filepath.Dir(root))
	}
	if homeDir != "" && (filepath.Clean(root) == filepath.Clean(homeDir) || filepath.Clean(root) == filepath.Clean(realHomeDir(homeDir))) {
		return loadHomeHarnessMCPs(root)
	}
	return nil
}

type homeMCPTarget struct {
	harness string
	relPath string
}

var homeMCPTargets = []homeMCPTarget{
	{harness: "antigravity", relPath: filepath.Join(".gemini", "config", "mcp_config.json")},
	{harness: "antigravity", relPath: filepath.Join(".gemini", "antigravity-cli", "settings.json")},
	{harness: "claude", relPath: ".claude.json"},
	{harness: "claude", relPath: filepath.Join(".claude", "mcp.json")},
	{harness: "claude", relPath: filepath.Join(".claude", "settings.json")},
	{harness: "cline", relPath: filepath.Join(".cline", "cline_mcp_settings.json")},
	{harness: "codex", relPath: filepath.Join(".codex", "config.toml")},
	{harness: "copilot", relPath: filepath.Join(".copilot", "mcp-config.json")},
	{harness: "fx", relPath: filepath.Join(".fx", "mcp.json")},
	{harness: "gemini", relPath: filepath.Join(".gemini", "settings.json")},
	{harness: "grok", relPath: filepath.Join(".grok", "config.toml")},
	{harness: "kilo", relPath: filepath.Join(".config", "kilo", "kilo.json")},
	{harness: "kimi-cli", relPath: filepath.Join(".kimi", "mcp.json")},
	{harness: "kimi-cli", relPath: filepath.Join(".kimi", "config.toml")},
	{harness: "kimi-code", relPath: filepath.Join(".kimi-code", "mcp.json")},
	{harness: "kimi-code", relPath: filepath.Join(".kimi-code", "config.toml")},
	{harness: "mcode", relPath: filepath.Join(".minimax", "mcp.json")},
	{harness: "muse", relPath: filepath.Join(".config", "muse", "settings.json")},
	{harness: "opencode", relPath: filepath.Join(".config", "opencode", "opencode.json")},
	{harness: "pi", relPath: filepath.Join(".pi", "agent", "mcp.json")},
	{harness: "qwen", relPath: filepath.Join(".qwen", "settings.json")},
	{harness: "vibe", relPath: filepath.Join(".vibe", "config.toml")},
}

func loadHomeHarnessMCPs(homeRoot string) []SharedMCP {
	if !isDir(homeRoot) {
		return nil
	}
	byName := make(map[string]*SharedMCP)
	for _, target := range homeMCPTargets {
		p := filepath.Join(homeRoot, target.relPath)
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		parsed := parseMCPFile(p, "harnesses", target.harness)
		for _, item := range parsed {
			mergeMCP(byName, item)
		}
	}
	return finalizeMCPMap(byName)
}

func loadDirectMCPs(root string) []SharedMCP {
	var files []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		full := filepath.Join(root, name)
		if !de.IsDir() {
			if strings.Contains(lower, "mcp") && (strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".toml")) {
				files = append(files, full)
			}
			continue
		}
		if lower == "mcp" || lower == "mcps" {
			subEntries, err := os.ReadDir(full)
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if strings.HasPrefix(sub.Name(), ".") {
					continue
				}
				subFull := filepath.Join(full, sub.Name())
				subLower := strings.ToLower(sub.Name())
				if !sub.IsDir() {
					if strings.HasSuffix(subLower, ".json") || strings.HasSuffix(subLower, ".toml") {
						files = append(files, subFull)
					}
					continue
				}
				for _, cand := range []string{"mcp.json", "mcp_config.json", "config.json", "config.toml"} {
					cp := filepath.Join(subFull, cand)
					if fi, err := os.Stat(cp); err == nil && !fi.IsDir() {
						files = append(files, cp)
					}
				}
			}
		}
	}
	sort.Strings(files)

	byName := make(map[string]*SharedMCP)
	for _, f := range files {
		parsed := parseMCPFile(f, "shared", "")
		for _, item := range parsed {
			mergeMCP(byName, item)
		}
	}
	return finalizeMCPMap(byName)
}

var siblingMCPCandidates = []string{
	"mcp.json",
	"mcp_config.json",
	"mcp-config.json",
	"cline_mcp_settings.json",
	"opencode.json",
	"kilo.json",
	"settings.json",
	"config.toml",
	"linux-config.toml",
}

func loadSiblingHarnessMCPs(harnessesRoot string) []SharedMCP {
	entries, err := os.ReadDir(harnessesRoot)
	if err != nil {
		return nil
	}
	byName := make(map[string]*SharedMCP)
	for _, de := range entries {
		name := de.Name()
		if strings.HasPrefix(name, ".") || name == "shared" {
			continue
		}
		dir := filepath.Join(harnessesRoot, name)
		if !isDir(dir) {
			continue
		}
		for _, cand := range siblingMCPCandidates {
			p := filepath.Join(dir, cand)
			fi, err := os.Stat(p)
			if err != nil || fi.IsDir() {
				continue
			}
			parsed := parseMCPFile(p, "harnesses", name)
			for _, item := range parsed {
				mergeMCP(byName, item)
			}
		}
	}
	return finalizeMCPMap(byName)
}

func mergeMCP(byName map[string]*SharedMCP, item SharedMCP) {
	key := strings.ToLower(strings.TrimSpace(item.Name))
	if key == "" {
		return
	}
	existing, ok := byName[key]
	if !ok {
		cp := item
		if cp.Env != nil {
			envCopy := make(map[string]string, len(cp.Env))
			for k, v := range cp.Env {
				envCopy[k] = v
			}
			cp.Env = envCopy
		}
		byName[key] = &cp
		return
	}

	// Merge harness provenance.
	for _, h := range item.Harnesses {
		if h == "" {
			continue
		}
		found := false
		for _, eh := range existing.Harnesses {
			if eh == h {
				found = true
				break
			}
		}
		if !found {
			existing.Harnesses = append(existing.Harnesses, h)
		}
	}

	// Prefer a direct non-shell command over a `sh -c` wrapper.
	if existing.Command == "" || (isShellWrapper(existing.Command) && item.Command != "" && !isShellWrapper(item.Command)) {
		existing.Command = item.Command
		existing.Args = append([]string(nil), item.Args...)
		if item.SourcePath != "" {
			existing.SourcePath = item.SourcePath
		}
		if item.RawJSON != "" {
			existing.RawJSON = item.RawJSON
		}
	}
	if existing.URL == "" && item.URL != "" {
		existing.URL = item.URL
	}
	if existing.Description == "" && item.Description != "" {
		existing.Description = item.Description
	}
	if existing.Exposure == "" && item.Exposure != "" {
		existing.Exposure = item.Exposure
	}
	if existing.Transport == "" && item.Transport != "" {
		existing.Transport = item.Transport
	}
	if len(item.Env) > 0 {
		if existing.Env == nil {
			existing.Env = make(map[string]string, len(item.Env))
		}
		for k, v := range item.Env {
			if _, exists := existing.Env[k]; !exists {
				existing.Env[k] = v
			}
		}
	}
	// Prefer a dedicated mcp.json source path when available.
	if !isDedicatedMCPFile(existing.SourcePath) && isDedicatedMCPFile(item.SourcePath) && !isShellWrapper(item.Command) {
		existing.SourcePath = item.SourcePath
		if item.RawJSON != "" {
			existing.RawJSON = item.RawJSON
		}
	}
}

func isShellWrapper(cmd string) bool {
	base := filepath.Base(strings.TrimSpace(cmd))
	return base == "sh" || base == "bash" || base == "zsh"
}

func isDedicatedMCPFile(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	return strings.Contains(lower, "mcp") && strings.HasSuffix(lower, ".json")
}

func finalizeMCPMap(byName map[string]*SharedMCP) []SharedMCP {
	out := make([]SharedMCP, 0, len(byName))
	for _, m := range byName {
		sort.Strings(m.Harnesses)
		if m.Transport == "" {
			if m.URL != "" {
				m.Transport = "http"
			} else if m.Command != "" {
				m.Transport = "stdio"
			}
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func parseMCPFile(path, sourceKind, harness string) []SharedMCP {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(path), ".toml") {
		return parseMCPTOML(string(data), path, sourceKind, harness)
	}
	return parseMCPJSON(data, path, sourceKind, harness)
}

func parseMCPJSON(data []byte, path, sourceKind, harness string) []SharedMCP {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}

	var out []SharedMCP
	collectMap := func(raw json.RawMessage) {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(raw, &servers); err != nil {
			return
		}
		names := make([]string, 0, len(servers))
		for k := range servers {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			if item, ok := parseMCPServerObject(name, servers[name], path, sourceKind, harness); ok {
				out = append(out, item)
			}
		}
	}

	if raw, ok := root["mcpServers"]; ok {
		collectMap(raw)
	}
	if raw, ok := root["mcp_servers"]; ok {
		collectMap(raw)
	}
	if raw, ok := root["servers"]; ok {
		collectMap(raw)
	}
	if raw, ok := root["mcp"]; ok {
		var mcpObj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &mcpObj); err == nil {
			if srvRaw, hasServers := mcpObj["servers"]; hasServers {
				collectMap(srvRaw)
			} else {
				// Kilo-style: "mcp": { "<server>": { "command": [...], ... } }
				collectMap(raw)
			}
		}
	}

	// Single standalone server definition file (only for direct shared configs).
	if len(out) == 0 && sourceKind == "shared" {
		if _, hasCmd := root["command"]; hasCmd {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if nameRaw, ok := root["name"]; ok {
				var s string
				if err := json.Unmarshal(nameRaw, &s); err == nil && strings.TrimSpace(s) != "" {
					name = strings.TrimSpace(s)
				}
			}
			if item, ok := parseMCPServerObject(name, data, path, sourceKind, harness); ok {
				out = append(out, item)
			}
		} else if _, hasURL := root["url"]; hasURL {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if nameRaw, ok := root["name"]; ok {
				var s string
				if err := json.Unmarshal(nameRaw, &s); err == nil && strings.TrimSpace(s) != "" {
					name = strings.TrimSpace(s)
				}
			}
			if item, ok := parseMCPServerObject(name, data, path, sourceKind, harness); ok {
				out = append(out, item)
			}
		}
	}
	return out
}

func parseMCPServerObject(name string, raw json.RawMessage, path, sourceKind, harness string) (SharedMCP, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return SharedMCP{}, false
	}

	item := SharedMCP{
		Name:       strings.TrimSpace(name),
		SourcePath: path,
		SourceKind: sourceKind,
	}
	if harness != "" {
		item.Harnesses = []string{harness}
	}

	if cmdRaw, ok := obj["command"]; ok {
		var cmdStr string
		if err := json.Unmarshal(cmdRaw, &cmdStr); err == nil {
			item.Command = strings.TrimSpace(cmdStr)
		} else {
			var cmdSlice []string
			if err := json.Unmarshal(cmdRaw, &cmdSlice); err == nil && len(cmdSlice) > 0 {
				item.Command = strings.TrimSpace(cmdSlice[0])
				if len(cmdSlice) > 1 {
					item.Args = cmdSlice[1:]
				}
			}
		}
	}
	if argsRaw, ok := obj["args"]; ok && len(item.Args) == 0 {
		var args []string
		if err := json.Unmarshal(argsRaw, &args); err == nil {
			item.Args = args
		}
	}
	for _, urlKey := range []string{"url", "serverUrl", "httpUrl"} {
		if uRaw, ok := obj[urlKey]; ok && item.URL == "" {
			var u string
			if err := json.Unmarshal(uRaw, &u); err == nil {
				item.URL = strings.TrimSpace(u)
			}
		}
	}

	// Must have at least a command or a URL to be a valid server entry (skips
	// unrelated keys like `"excluded": []` under Gemini's `"mcp"` block).
	if item.Command == "" && item.URL == "" {
		return SharedMCP{}, false
	}

	for _, tKey := range []string{"transport", "type"} {
		if tRaw, ok := obj[tKey]; ok && item.Transport == "" {
			var t string
			if err := json.Unmarshal(tRaw, &t); err == nil {
				item.Transport = strings.TrimSpace(t)
			}
		}
	}
	if item.Transport == "" || item.Transport == "local" {
		if item.URL != "" {
			item.Transport = "http"
		} else {
			item.Transport = "stdio"
		}
	}
	if dRaw, ok := obj["description"]; ok {
		var d string
		if err := json.Unmarshal(dRaw, &d); err == nil {
			item.Description = normalizeInline(d)
		}
	}
	if eRaw, ok := obj["exposure"]; ok {
		var e string
		if err := json.Unmarshal(eRaw, &e); err == nil {
			item.Exposure = strings.TrimSpace(e)
		}
	}
	if disRaw, ok := obj["disabled"]; ok {
		var dis bool
		if err := json.Unmarshal(disRaw, &dis); err == nil {
			item.Disabled = dis
		}
	}
	if enRaw, ok := obj["enabled"]; ok {
		var en bool
		if err := json.Unmarshal(enRaw, &en); err == nil && !en {
			item.Disabled = true
		}
	}
	if envRaw, ok := obj["env"]; ok {
		var env map[string]string
		if err := json.Unmarshal(envRaw, &env); err == nil && len(env) > 0 {
			item.Env = env
		}
	}

	var pretty any
	if err := json.Unmarshal(raw, &pretty); err == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			item.RawJSON = string(b)
		}
	}
	return item, true
}

// parseMCPTOML extracts `[mcp_servers.<name>]` tables and `[[mcp_servers]]`
// array-of-table blocks without external TOML dependencies.
func parseMCPTOML(text, path, sourceKind, harness string) []SharedMCP {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []SharedMCP
	var cur *SharedMCP

	flush := func() {
		if cur == nil {
			return
		}
		if cur.Name != "" && (cur.Command != "" || cur.URL != "") {
			if cur.Transport == "" {
				if cur.URL != "" {
					cur.Transport = "http"
				} else {
					cur.Transport = "stdio"
				}
			}
			if cur.RawJSON == "" {
				if b, err := json.MarshalIndent(map[string]any{
					"command":   cur.Command,
					"args":      cur.Args,
					"url":       cur.URL,
					"transport": cur.Transport,
				}, "", "  "); err == nil {
					cur.RawJSON = string(b)
				}
			}
			out = append(out, *cur)
		}
		cur = nil
	}

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(stripTOMLComment(lines[i]))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "[[") && strings.HasSuffix(trimmed, "]]") {
			flush()
			sec := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
			if sec == "mcp_servers" {
				cur = &SharedMCP{SourcePath: path, SourceKind: sourceKind}
				if harness != "" {
					cur.Harnesses = []string{harness}
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			flush()
			sec := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			if strings.HasPrefix(sec, "mcp_servers.") {
				rest := strings.TrimPrefix(sec, "mcp_servers.")
				// Ignore sub-tables like [mcp_servers.foo.env].
				if !strings.Contains(rest, ".") {
					name := parseYAMLScalar(rest)
					cur = &SharedMCP{Name: name, SourcePath: path, SourceKind: sourceKind}
					if harness != "" {
						cur.Harnesses = []string{harness}
					}
				}
			}
			continue
		}
		if cur == nil {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		val := strings.TrimSpace(trimmed[eq+1:])
		// Join multiline arrays.
		if strings.HasPrefix(val, "[") && !strings.Contains(val, "]") {
			var buf strings.Builder
			buf.WriteString(val)
			for i+1 < len(lines) {
				i++
				part := strings.TrimSpace(stripTOMLComment(lines[i]))
				buf.WriteByte(' ')
				buf.WriteString(part)
				if strings.Contains(part, "]") {
					break
				}
			}
			val = buf.String()
		}
		switch key {
		case "name":
			cur.Name = parseYAMLScalar(val)
		case "command":
			cur.Command = parseYAMLScalar(val)
		case "url":
			cur.URL = parseYAMLScalar(val)
		case "transport", "type":
			cur.Transport = parseYAMLScalar(val)
		case "description":
			cur.Description = normalizeInline(parseYAMLScalar(val))
		case "args":
			if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
				cur.Args = parseYAMLInlineList(val)
			}
		case "disabled":
			cur.Disabled = strings.EqualFold(val, "true")
		case "enabled":
			if strings.EqualFold(val, "false") {
				cur.Disabled = true
			}
		}
	}
	flush()
	return out
}

func stripTOMLComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inDouble {
			escaped = true
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
		} else if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '#' && !inSingle && !inDouble {
			return line[:i]
		}
	}
	return line
}

// ---------------------------------------------------------------------------
// Fuzzy scoring
// ---------------------------------------------------------------------------

// FuzzyScoreSharedSkill evaluates multi-token AND fuzzy matching across a skill's
// name, directory slug, description, and paths.
func FuzzyScoreSharedSkill(sk SharedSkill, tokens []string) (int, []int, bool) {
	if len(tokens) == 0 {
		return 0, nil, true
	}
	type field struct {
		text   string
		mult   int
		base   int
		isName bool
	}
	fields := []field{
		{text: sk.Name, mult: 4, base: 260, isName: true},
		{text: sk.DirName, mult: 3, base: 200},
		{text: sk.Description, mult: 2, base: 100},
		{text: strings.Join(sk.Paths, " "), mult: 1, base: 60},
	}
	return scoreSharedFields(tokens, func(yield func(string, int, int, bool)) {
		for _, f := range fields {
			yield(f.text, f.mult, f.base, f.isName)
		}
	})
}

// FuzzyScoreSharedMCP evaluates multi-token AND fuzzy matching across an MCP
// server's name, description, command/args/URL, transport, and harnesses.
func FuzzyScoreSharedMCP(m SharedMCP, tokens []string) (int, []int, bool) {
	if len(tokens) == 0 {
		return 0, nil, true
	}
	type field struct {
		text   string
		mult   int
		base   int
		isName bool
	}
	fields := []field{
		{text: m.Name, mult: 4, base: 260, isName: true},
		{text: m.Description, mult: 2, base: 120},
		{text: m.CommandLine(), mult: 2, base: 100},
		{text: m.Transport, mult: 2, base: 80},
		{text: strings.Join(m.Harnesses, " "), mult: 1, base: 60},
		{text: m.SourceKind, mult: 1, base: 50},
	}
	return scoreSharedFields(tokens, func(yield func(string, int, int, bool)) {
		for _, f := range fields {
			yield(f.text, f.mult, f.base, f.isName)
		}
	})
}

func scoreSharedFields(tokens []string, iter func(yield func(text string, mult, base int, isName bool))) (int, []int, bool) {
	total := 0
	var namePosSet map[int]struct{}
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		best := math.MinInt32
		matched := false
		iter(func(text string, mult, base int, isName bool) {
			if text == "" {
				return
			}
			s, pos, ok := FuzzyMatch(tok, text)
			if !ok {
				return
			}
			matched = true
			if isName && len(pos) > 0 {
				if namePosSet == nil {
					namePosSet = make(map[int]struct{}, len(pos))
				}
				for _, idx := range pos {
					namePosSet[idx] = struct{}{}
				}
			}
			w := base + s*mult
			if w > best {
				best = w
			}
		})
		if !matched {
			return 0, nil, false
		}
		total += best
	}
	var highlights []int
	if len(namePosSet) > 0 {
		highlights = make([]int, 0, len(namePosSet))
		for idx := range namePosSet {
			highlights = append(highlights, idx)
		}
		sort.Ints(highlights)
	}
	return total, highlights, true
}
