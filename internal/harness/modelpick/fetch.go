package modelpick

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/arhammahajan/spaces/internal/harness/routerctl"
)

// fetchBodyLimit caps the listing download, like routerctl's queries.
const fetchBodyLimit = 8 << 20

// Fetch performs the picker's own GET {opts.Router.URL}/v1/models with
// Authorization: Bearer <opts.Router.APIKey> when the key is non-empty,
// and parses every optional metadata.router fact tolerantly: nulls,
// missing keys, and unknown shapes leave the corresponding Model field
// at its zero value instead of failing the listing.
func Fetch(opts routerctl.Options) ([]Model, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.Router.URL), "/")
	if base == "" {
		return nil, fmt.Errorf("no router URL configured")
	}
	timeout := opts.HTTPTimeout
	if timeout <= 0 {
		timeout = routerctl.DefaultHTTPTimeout
	}
	req, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid router URL %q: %w", opts.Router.URL, err)
	}
	if key := opts.Router.APIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s/v1/models: %w", base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("get %s/v1/models: %w", base, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s/v1/models: status %d", base, resp.StatusCode)
	}
	models, err := parseModels(body)
	if err != nil {
		return nil, fmt.Errorf("get %s/v1/models: %w", base, err)
	}
	return models, nil
}

// parseModels decodes a /v1/models body, tolerating both the
// {"data": [...]} envelope and a bare [...] array, and skipping rows
// without an id.
func parseModels(body []byte) ([]Model, error) {
	raw := json.RawMessage(body)
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 {
		raw = envelope.Data
	}
	var rows []struct {
		ID       string          `json:"id"`
		OwnedBy  string          `json:"owned_by"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("parse models listing: %w", err)
	}
	models := make([]Model, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		m := Model{ID: row.ID, OwnedBy: row.OwnedBy}
		applyRouterFacts(row.Metadata, &m)
		models = append(models, m)
	}
	return models, nil
}

// applyRouterFacts reads the optional metadata.router facts onto m.
// Every access tolerates absence: null metadata, a missing or null
// router key, missing limits/reasoning/modalities/freshness blocks,
// and null or unknown-shaped values all leave fields untouched.
func applyRouterFacts(meta json.RawMessage, m *Model) {
	router := asObject(member(asObject(meta), "router"))
	if router == nil {
		return
	}
	limits := asObject(member(router, "limits"))
	m.ContextWindow = factInt(member(limits, "context_window"))
	m.MaxOutput = factInt(member(limits, "max_output_tokens"))
	m.Reasoning = reasoningHint(member(router, "reasoning"))
	m.Modalities = inputModalities(member(router, "modalities"))
	m.Stale = discoveryStale(router)
}

// asObject decodes raw as a JSON object, returning nil for null,
// non-objects, empty input, and malformed JSON.
func asObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return obj
}

// member returns obj[key], or nil when obj is nil, the key is absent,
// or the value is JSON null.
func member(obj map[string]json.RawMessage, key string) json.RawMessage {
	if obj == nil {
		return nil
	}
	raw, ok := obj[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// factInt reads the numeric .value of a router fact object like
// {"value": 1000000, "source": "...", "kind": "reviewed"}. Only a raw
// JSON number counts: the literal is parsed as an exact integer, so
// values beyond float64 precision survive. Null values, non-numeric
// shapes (including quoted numbers), and negative numbers read as 0
// (unknown).
func factInt(raw json.RawMessage) int64 {
	val := member(asObject(raw), "value")
	if len(val) == 0 || (val[0] != '-' && (val[0] < '0' || val[0] > '9')) {
		return 0
	}
	var num json.Number
	if err := json.Unmarshal(val, &num); err != nil {
		return 0
	}
	i, err := num.Int64()
	if err != nil || i < 0 {
		return 0
	}
	return i
}

// factBool reads the .value of a fact object as a bool. The second
// return is false for absent, null, and non-bool shapes.
func factBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(member(asObject(raw), "value"), &b); err != nil {
		return false, false
	}
	return b, true
}

// factStrings reads the .value of a fact object as a list of strings,
// skipping non-string entries. Null, missing, and non-list shapes read
// as nil.
func factStrings(raw json.RawMessage) []string {
	var items []json.RawMessage
	if err := json.Unmarshal(member(asObject(raw), "value"), &items); err != nil {
		return nil
	}
	var out []string
	for _, item := range items {
		var s string
		if json.Unmarshal(item, &s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// reasoningHint renders the compact hint from the metadata.router
// reasoning block: the effort enum joined by commas, or "supported"
// when reasoning is supported without an effort enum. Absent, null, and
// unknown shapes read as "".
func reasoningHint(raw json.RawMessage) string {
	reasoning := asObject(raw)
	if reasoning == nil {
		return ""
	}
	if effort := asObject(member(reasoning, "effort")); effort != nil {
		if vals := factStrings(member(effort, "values")); len(vals) > 0 {
			return strings.Join(vals, ",")
		}
	}
	if b, ok := factBool(member(reasoning, "supported")); ok && b {
		return "supported"
	}
	return ""
}

// modalityOrder is the frozen input-modality vocabulary order used to
// render input lists compactly; it matches the router's modality facts.
var modalityOrder = []string{"text", "image", "audio", "video", "pdf", "embedding"}

// inputModalities lists the metadata.router.modalities input facts that
// are true, in vocabulary order with unknown modality names sorted
// after it. Null, missing, and non-object shapes read as nil.
func inputModalities(raw json.RawMessage) []string {
	input := asObject(member(asObject(raw), "input"))
	if input == nil {
		return nil
	}
	enabled := make(map[string]bool, len(input))
	var extra []string
	for name, fact := range input {
		if b, ok := factBool(fact); ok && b {
			enabled[name] = true
			if !knownModality(name) {
				extra = append(extra, name)
			}
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	sort.Strings(extra)
	out := make([]string, 0, len(enabled))
	for _, name := range modalityOrder {
		if enabled[name] {
			out = append(out, name)
		}
	}
	return append(out, extra...)
}

func knownModality(name string) bool {
	for _, m := range modalityOrder {
		if m == name {
			return true
		}
	}
	return false
}

// discoveryStale reads metadata.router.freshness.discovery.stale; null,
// missing, and non-bool shapes are not stale.
func discoveryStale(router map[string]json.RawMessage) bool {
	var stale bool
	discovery := asObject(member(asObject(member(router, "freshness")), "discovery"))
	if err := json.Unmarshal(member(discovery, "stale"), &stale); err != nil {
		return false
	}
	return stale
}
