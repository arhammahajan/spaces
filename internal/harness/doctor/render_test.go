package doctor

import (
	"encoding/json"
	"strings"
	"testing"
)

// Column math for the golden below: the state column is 4 wide plus a
// two-space separator; the name column is 13 (the longest name,
// "router-config") plus a two-space separator.
func TestRenderGolden(t *testing.T) {
	checks := []Check{
		{Name: "router", State: StateOK, Detail: "live and ready; 12 models"},
		{Name: "router-config", State: StateOK, Detail: "valid config at /tmp/router.toml"},
		{Name: "profiles", State: StateWarn, Detail: "0 profiles"},
		{Name: "t3-settings", State: StateFail, Detail: "parse /x/.t3/userdata/settings.json: bad"},
	}
	want := "ok    router         live and ready; 12 models\n" +
		"ok    router-config  valid config at /tmp/router.toml\n" +
		"warn  profiles       0 profiles\n" +
		"fail  t3-settings    parse /x/.t3/userdata/settings.json: bad\n" +
		"2 ok, 1 warn, 1 fail\n"
	if got := Render(checks); got != want {
		t.Errorf("Render mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRenderEmptyDetailTrimsTrailingSpaces(t *testing.T) {
	checks := []Check{{Name: "x", State: StateOK}}
	got := Render(checks)
	want := "ok    x\n1 ok, 0 warn, 0 fail\n"
	if got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderEmpty(t *testing.T) {
	if got := Render(nil); got != "0 ok, 0 warn, 0 fail\n" {
		t.Errorf("Render(nil) = %q", got)
	}
}

func TestSummarize(t *testing.T) {
	checks := []Check{
		{State: StateOK}, {State: StateOK}, {State: StateOK},
		{State: StateWarn}, {State: StateWarn},
		{State: StateFail},
		{State: "unknown"}, // not counted
	}
	s := Summarize(checks)
	if s.OK != 3 || s.Warn != 2 || s.Fail != 1 {
		t.Errorf("Summarize = %+v, want 3/2/1", s)
	}
}

func TestRenderJSONGolden(t *testing.T) {
	checks := []Check{{Name: "router", State: StateOK, Detail: "live and ready; 2 models"}}
	out, err := RenderJSON(checks)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "checks": [
    {
      "name": "router",
      "state": "ok",
      "detail": "live and ready; 2 models"
    }
  ],
  "summary": {
    "ok": 1,
    "warn": 0,
    "fail": 0
  }
}
`
	if out != want {
		t.Errorf("RenderJSON mismatch:\ngot:  %q\nwant: %q", out, want)
	}
}

func TestRenderJSONShape(t *testing.T) {
	checks := []Check{
		{Name: "router", State: StateOK, Detail: "live and ready; 2 models"},
		{Name: "profiles", State: StateFail, Detail: "profiles: parse"},
	}
	out, err := RenderJSON(checks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("RenderJSON must end with a newline")
	}

	// Round-trip through the typed struct.
	var rep Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(rep.Checks) != 2 {
		t.Fatalf("report has %d checks, want 2", len(rep.Checks))
	}
	if rep.Checks[0].Name != "router" || rep.Checks[0].State != StateOK ||
		rep.Checks[0].Detail != "live and ready; 2 models" {
		t.Errorf("first check = %+v", rep.Checks[0])
	}
	if rep.Checks[1].State != StateFail {
		t.Errorf("second check = %+v", rep.Checks[1])
	}
	if rep.Summary.OK != 1 || rep.Summary.Warn != 0 || rep.Summary.Fail != 1 {
		t.Errorf("summary = %+v, want 1/0/1", rep.Summary)
	}

	// The typed shape carries exactly the documented keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Errorf("top-level keys = %v, want exactly checks and summary", keysOf(raw))
	}
	if _, ok := raw["checks"]; !ok {
		t.Error("missing checks key")
	}
	var rawSummary map[string]json.RawMessage
	if err := json.Unmarshal(raw["summary"], &rawSummary); err != nil {
		t.Fatal(err)
	}
	if len(rawSummary) != 3 {
		t.Errorf("summary keys = %v, want ok/warn/fail", keysOf(rawSummary))
	}
	for _, k := range []string{"ok", "warn", "fail"} {
		if _, ok := rawSummary[k]; !ok {
			t.Errorf("summary missing key %q", k)
		}
	}
	var rawChecks []map[string]json.RawMessage
	if err := json.Unmarshal(raw["checks"], &rawChecks); err != nil {
		t.Fatal(err)
	}
	if len(rawChecks) != 2 {
		t.Fatalf("checks array has %d entries, want 2", len(rawChecks))
	}
	if len(rawChecks[0]) != 3 {
		t.Errorf("check keys = %v, want name/state/detail", keysOf(rawChecks[0]))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
