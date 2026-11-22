package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
	fc := &FakeClient{Up: true}
	fc.Snap = serverSnapshotForStatus()
	env := testEnv(fc)

	if code := Run(nil, env); code != ExitOK {
		t.Errorf("bare = %d", code)
	}
	if got := stdoutOf(t, env); !strings.Contains(got, "usage:") {
		t.Errorf("bare help = %q", got)
	}
	if code := Run([]string{"bogus"}, env); code != ExitUsage {
		t.Errorf("bogus = %d", code)
	}
	if code := Run([]string{"help"}, env); code != ExitOK {
		t.Errorf("help = %d", code)
	}
	if code := Run([]string{"help", "wait"}, env); code != ExitOK {
		t.Errorf("help wait = %d", code)
	}
	if code := Run([]string{"help", "bogus"}, env); code != ExitUsage {
		t.Errorf("help bogus = %d", code)
	}
	if code := Run([]string{"--help"}, env); code != ExitOK {
		t.Errorf("--help = %d", code)
	}
}

func TestRunNoClient(t *testing.T) {
	env := testEnv(nil)
	if code := Run([]string{"panes"}, env); code != ExitError {
		t.Errorf("panes without client = %d", code)
	}
	if code := Run([]string{"help"}, env); code != ExitOK {
		t.Errorf("help without client = %d", code)
	}
}

func TestParseFlags(t *testing.T) {
	f, err := parseFlags([]string{"--session", "s1", "--json", "pos"}, []string{"session"}, []string{"json"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.str["session"] != "s1" || !f.set["json"] || len(f.pos) != 1 || f.pos[0] != "pos" {
		t.Errorf("parsed = %+v", f)
	}
	if _, err := parseFlags([]string{"--bogus"}, nil, nil, nil); err == nil {
		t.Errorf("unknown flag should error")
	}
	if _, err := parseFlags([]string{"--session"}, []string{"session"}, nil, nil); err == nil {
		t.Errorf("missing value should error")
	}
	if _, err := parseFlags([]string{"--json=x"}, nil, []string{"json"}, nil); err == nil {
		t.Errorf("bool with value should error")
	}
	f, err = parseFlags([]string{"-y", "pos"}, nil, []string{"yes"}, map[string]string{"y": "yes"})
	if err != nil || !f.set["yes"] {
		t.Errorf("alias = %+v %v", f, err)
	}
	f, err = parseFlags([]string{"--", "--json"}, nil, []string{"json"}, nil)
	if err != nil || len(f.pos) != 1 || f.pos[0] != "--json" {
		t.Errorf("double-dash = %+v %v", f, err)
	}
	if _, err := parseFlags([]string{"--help"}, nil, nil, nil); !errors.Is(err, errHelp) {
		t.Errorf("help = %v", err)
	}
}

func TestBucketOf(t *testing.T) {
	cases := map[string]string{
		"blocked": BucketNeeds, "needs_you": BucketNeeds,
		"failed": BucketFailed, "error": BucketFailed,
		"done": BucketDone, "working": BucketWorking,
		"": BucketIdle, "idle": BucketIdle, "weird": BucketIdle,
	}
	for in, want := range cases {
		if got := bucketOf(in); got != want {
			t.Errorf("bucketOf(%q) = %q want %q", in, got, want)
		}
	}
	if bucketPriority(BucketNeeds) >= bucketPriority(BucketFailed) ||
		bucketPriority(BucketFailed) >= bucketPriority(BucketDone) {
		t.Errorf("priority order broken")
	}
}

func TestCanonicalApp(t *testing.T) {
	if canonicalApp("claude") != "claude-code" || canonicalApp("cursor") != "cursor-agent" {
		t.Errorf("canonical mapping broken")
	}
	if canonicalApp("codex") != "codex" {
		t.Errorf("passthrough broken")
	}
}

func TestInboxCommand(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)

	if code := Run([]string{"inbox", "--help"}, env); code != ExitOK {
		t.Errorf("inbox --help = %d", code)
	}
	if code := Run([]string{"help", "inbox"}, env); code != ExitOK {
		t.Errorf("help inbox = %d", code)
	}
	if code := Run([]string{"inbox"}, env); code != ExitError {
		t.Errorf("inbox without RunInbox = %d, want ExitError", code)
	}
	called := false
	env.RunInbox = func(_ Env, _ []string) int {
		called = true
		return ExitOK
	}
	if code := Run([]string{"inbox"}, env); code != ExitOK || !called {
		t.Errorf("inbox wired = %d called=%v", code, called)
	}
	if code := Run([]string{"inbox", "extra"}, env); code != ExitUsage {
		t.Errorf("inbox with extra positional = %d, want ExitUsage", code)
	}
}

func TestConversationsCommand(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)

	if code := Run([]string{"conversations", "--help"}, env); code != ExitOK {
		t.Errorf("conversations --help = %d", code)
	}
	if code := Run([]string{"chats", "-h"}, env); code != ExitOK {
		t.Errorf("chats -h = %d", code)
	}
	if code := Run([]string{"help", "conversations"}, env); code != ExitOK {
		t.Errorf("help conversations = %d", code)
	}
	if code := Run([]string{"help", "chats"}, env); code != ExitOK {
		t.Errorf("help chats = %d", code)
	}
	if code := Run([]string{"conversations"}, env); code != ExitError {
		t.Errorf("conversations without RunConversations = %d, want ExitError", code)
	}

	var gotArgs []string
	env.RunConversations = func(_ Env, args []string) int {
		gotArgs = append([]string(nil), args...)
		return ExitOK
	}
	if code := Run([]string{"chats", "--query", "@claude retry"}, env); code != ExitOK {
		t.Errorf("chats wired = %d", code)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "--query" || gotArgs[1] != "@claude retry" {
		t.Errorf("gotArgs = %v", gotArgs)
	}
	if code := Run([]string{"conversations", "extra"}, env); code != ExitUsage {
		t.Errorf("conversations with extra positional = %d, want ExitUsage", code)
	}
}

func TestSharedCommand(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)

	for _, cmd := range []string{"shared", "skills", "mcp"} {
		if code := Run([]string{cmd, "--help"}, env); code != ExitOK {
			t.Errorf("%s --help = %d", cmd, code)
		}
		if code := Run([]string{"help", cmd}, env); code != ExitOK {
			t.Errorf("help %s = %d", cmd, code)
		}
	}
	if code := Run([]string{"shared"}, env); code != ExitError {
		t.Errorf("shared without RunShared = %d, want ExitError", code)
	}

	var gotArgs []string
	env.RunShared = func(_ Env, args []string) int {
		gotArgs = append([]string(nil), args...)
		return ExitOK
	}
	if code := Run([]string{"skills", "--dir", "/tmp/shared", "--query", "browser"}, env); code != ExitOK {
		t.Errorf("skills wired = %d", code)
	}
	if len(gotArgs) != 4 || gotArgs[0] != "--dir" || gotArgs[1] != "/tmp/shared" || gotArgs[2] != "--query" || gotArgs[3] != "browser" {
		t.Errorf("gotArgs = %v", gotArgs)
	}
	if code := Run([]string{"shared", "extra"}, env); code != ExitUsage {
		t.Errorf("shared with extra positional = %d, want ExitUsage", code)
	}
}

func TestTabsCommand(t *testing.T) {
	fc := &FakeClient{Up: true}
	env := testEnv(fc)

	for _, cmd := range []string{"tabs", "tab"} {
		if code := Run([]string{cmd, "--help"}, env); code != ExitOK {
			t.Errorf("%s --help = %d", cmd, code)
		}
		if code := Run([]string{"help", cmd}, env); code != ExitOK {
			t.Errorf("help %s = %d", cmd, code)
		}
	}
	if code := Run([]string{"tabs"}, env); code != ExitError {
		t.Errorf("tabs without RunTabs = %d, want ExitError", code)
	}

	var gotArgs []string
	env.RunTabs = func(_ Env, args []string) int {
		gotArgs = append([]string(nil), args...)
		return ExitOK
	}
	if code := Run([]string{"tab", "--new", "--session", "model-router", "--query", "auth"}, env); code != ExitOK {
		t.Errorf("tab wired = %d", code)
	}
	if len(gotArgs) != 5 || gotArgs[0] != "--new" || gotArgs[1] != "--session" || gotArgs[2] != "model-router" || gotArgs[3] != "--query" || gotArgs[4] != "auth" {
		t.Errorf("gotArgs = %v", gotArgs)
	}
	if code := Run([]string{"tabs", "extra"}, env); code != ExitUsage {
		t.Errorf("tabs with extra positional = %d, want ExitUsage", code)
	}
}
