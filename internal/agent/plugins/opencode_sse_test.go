package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arhammahajan/spaces/internal/agent"
)

type chunkedReader struct {
	data  []byte
	chunk int
	off   int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.off >= len(c.data) {
		return 0, io.EOF
	}
	n := c.chunk
	if n <= 0 || n > len(p) {
		n = len(p)
	}
	if rem := len(c.data) - c.off; n > rem {
		n = rem
	}
	copy(p[:n], c.data[c.off:c.off+n])
	c.off += n
	return n, nil
}

func TestParseSSEStream(t *testing.T) {
	type sseMsg struct {
		event string
		data  string
	}

	raw := strings.Join([]string{
		": keepalive comment",
		"id: 1",
		"retry: 1000",
		"event: message",
		"data: line1",
		"data: line2",
		"",
		"data:{\"type\":\"session.busy\"}\r",
		"\r",
		": another comment\r",
		"event: custom\r",
		"data: first\r",
		"data: second\r",
		"\r",
	}, "\n")

	// Feed 3 bytes at a time to stress chunk boundaries.
	r := &chunkedReader{data: []byte(raw), chunk: 3}
	var got []sseMsg
	err := ParseSSEStream(r, func(eventName, data string) error {
		got = append(got, sseMsg{event: eventName, data: data})
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSEStream error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(got), got)
	}
	if got[0].event != "message" || got[0].data != "line1\nline2" {
		t.Errorf("event[0] = %+v, want event=message data='line1\\nline2'", got[0])
	}
	if got[1].event != "" || got[1].data != `{"type":"session.busy"}` {
		t.Errorf("event[1] = %+v, want empty event and json data", got[1])
	}
	if got[2].event != "custom" || got[2].data != "first\nsecond" {
		t.Errorf("event[2] = %+v, want event=custom data='first\\nsecond'", got[2])
	}
}

func TestParseSSEStreamLargeLine(t *testing.T) {
	// Line larger than bufio default 64KiB buffer, within 4MiB cap.
	big := strings.Repeat("x", 200*1024)
	stream := "data: " + big + "\n\n"
	var got string
	err := ParseSSEStream(strings.NewReader(stream), func(_, data string) error {
		got = data
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSEStream large line error: %v", err)
	}
	if len(got) != len(big) {
		t.Fatalf("got len %d, want %d", len(got), len(big))
	}
}

func TestOpenCodeEventMapper_AllEventTypes(t *testing.T) {
	mapper := NewOpenCodeEventMapper(agent.DaemonWatchTarget{
		CWD:     "/repo/app",
		Profile: "work",
	})
	mapper.nowFn = func() int64 { return 1700000000000 }

	mustMapOne := func(t *testing.T, payload string) agent.DaemonEvent {
		t.Helper()
		evs := mapper.Map("", payload)
		if len(evs) != 1 {
			t.Fatalf("expected 1 event for %s, got %d: %+v", payload, len(evs), evs)
		}
		ev := evs[0]
		if ev.Agent != "opencode" || ev.App != "opencode" || ev.Source != "daemon_sse" {
			t.Errorf("bad envelope identity: %+v", ev)
		}
		if ev.Profile != "work" {
			t.Errorf("Profile = %q, want work", ev.Profile)
		}
		return ev
	}

	t.Run("session.created and updated", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"session.created","properties":{"info":{"id":"ses_root","title":"Fix auth","directory":"/repo/auth"}}}`)
		if ev.Event != "session_start" || ev.State != "idle" || ev.SessionID != "ses_root" || ev.Title != "Fix auth" || ev.CWD != "/repo/auth" {
			t.Errorf("unexpected session.created event: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"session.updated","properties":{"info":{"id":"ses_root","title":"Fix auth v2"}}}`)
		if ev.Event != "session_updated" || ev.State != "" || ev.Title != "Fix auth v2" || ev.SessionID != "ses_root" {
			t.Errorf("unexpected session.updated event: %+v", ev)
		}
	})

	t.Run("session.status busy idle retry", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"busy"}}}`)
		if ev.Event != "prompt" || ev.State != "working" {
			t.Errorf("status busy: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"retry","message":"rate limited","attempt":2}}}`)
		if ev.State != "working" || !strings.Contains(ev.Text, "rate limited") {
			t.Errorf("status retry: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"session.status","properties":{"sessionID":"ses_root","status":{"type":"idle"}}}`)
		if ev.Event != "stop" || ev.State != "done" {
			t.Errorf("status idle: %+v", ev)
		}
	})

	t.Run("session.busy idle error execution variants", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"session.busy","properties":{"sessionID":"ses_root"}}`)
		if ev.Event != "prompt" || ev.State != "working" {
			t.Errorf("session.busy: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"session.idle","properties":{"sessionID":"ses_root"}}`)
		if ev.Event != "stop" || ev.State != "done" {
			t.Errorf("session.idle: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"session.error","properties":{"sessionID":"ses_root","error":{"name":"APIError","data":{"message":"upstream 500"}}}}`)
		if ev.Event != "stop_failure" || ev.State != "failed" || !strings.Contains(ev.Text, "upstream 500") {
			t.Errorf("session.error: %+v", ev)
		}
	})

	t.Run("tool.execute.before and after", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"tool.execute.before","properties":{"sessionID":"ses_root","tool":"bash","args":{"command":"go test ./..."}}}`)
		if ev.Event != "pre_tool" || ev.State != "working" || ev.Tool != "bash" || !strings.Contains(ev.Text, "go test") || !strings.Contains(ev.ToolInput, "go test") {
			t.Errorf("tool.execute.before: %+v", ev)
		}

		// Ask-user tool transitions to blocked/question.
		ev = mustMapOne(t, `{"type":"tool.execute.before","properties":{"sessionID":"ses_root","tool":"question","args":{"question":"Continue?"}}}`)
		if ev.Event != "pre_tool" || ev.State != "blocked" || ev.Kind != "question" {
			t.Errorf("ask tool.execute.before: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"tool.execute.after","properties":{"sessionID":"ses_root","tool":"bash","args":{"command":"go test ./..."},"output":"ok"}}`)
		if ev.Event != "post_tool" || ev.State != "working" || ev.Tool != "bash" || ev.ToolOutput != "ok" {
			t.Errorf("tool.execute.after: %+v", ev)
		}
	})

	t.Run("permission asked and replied", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"permission.asked","properties":{"sessionID":"ses_root","permission":"bash","patterns":["rm -rf /tmp/x"],"title":"Run shell command"}}`)
		if ev.Event != "permission" || ev.State != "blocked" || ev.Kind != "permission" || ev.Tool != "bash" || !strings.Contains(ev.Text, "rm -rf /tmp/x") {
			t.Errorf("permission.asked: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"permission.replied","properties":{"sessionID":"ses_root","reply":"once"}}`)
		if ev.State != "working" {
			t.Errorf("permission.replied: %+v", ev)
		}
	})

	t.Run("question asked and replied", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"question.asked","properties":{"sessionID":"ses_root","questions":[{"question":"Which DB should we use?"}]}}`)
		if ev.Event != "elicitation" || ev.State != "blocked" || ev.Kind != "question" || !strings.Contains(ev.Text, "Which DB") {
			t.Errorf("question.asked: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"question.replied","properties":{"sessionID":"ses_root"}}`)
		if ev.Event != "elicitation_result" || ev.State != "working" {
			t.Errorf("question.replied: %+v", ev)
		}
	})

	t.Run("message.updated user and assistant with usage", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"message.updated","properties":{"info":{"id":"msg_u1","sessionID":"ses_root","role":"user","text":"Fix the bug"}}}`)
		if ev.Event != "prompt" || ev.State != "working" || ev.Prompt != "Fix the bug" {
			t.Errorf("user message.updated: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"message.updated","properties":{"info":{"id":"msg_a1","sessionID":"ses_root","role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-4","cost":0.042,"tokens":{"input":1200,"output":350,"reasoning":80,"cache":{"read":500,"write":100}},"time":{"created":1700000001000,"completed":1700000005000}}}}`)
		if ev.Model != "anthropic/claude-sonnet-4" {
			t.Errorf("Model = %q, want anthropic/claude-sonnet-4", ev.Model)
		}
		if ev.Usage == nil {
			t.Fatalf("expected Usage to be populated")
		}
		if ev.Usage.InputTokens != 1200 || ev.Usage.OutputTokens != 350 || ev.Usage.ReasoningTokens != 80 ||
			ev.Usage.CacheReadTokens != 500 || ev.Usage.CacheWriteTokens != 100 || ev.Usage.CostUSD != 0.042 {
			t.Errorf("unexpected Usage: %+v", ev.Usage)
		}
		// Completed assistant message update should not override done back to working.
		if ev.State != "" {
			t.Errorf("expected empty state for completed assistant usage update, got %q", ev.State)
		}

		// Assistant error
		ev = mustMapOne(t, `{"type":"message.updated","properties":{"info":{"id":"msg_a2","sessionID":"ses_root","role":"assistant","modelID":"gpt-5","error":{"message":"context window exceeded"}}}}`)
		if ev.Event != "stop_failure" || ev.State != "failed" || ev.Model != "gpt-5" || !strings.Contains(ev.Text, "context window exceeded") {
			t.Errorf("assistant error message.updated: %+v", ev)
		}
	})

	t.Run("message.part.updated tool text subtask", func(t *testing.T) {
		ev := mustMapOne(t, `{"type":"message.part.updated","properties":{"part":{"type":"tool","sessionID":"ses_root","messageID":"msg_a1","tool":"read","state":{"status":"running","input":{"filePath":"main.go"}}}}}`)
		if ev.Event != "pre_tool" || ev.State != "working" || ev.Tool != "read" {
			t.Errorf("tool running part: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"message.part.updated","properties":{"part":{"type":"tool","sessionID":"ses_root","messageID":"msg_a1","tool":"read","state":{"status":"completed","input":{"filePath":"main.go"},"output":"package main"}}}}`)
		if ev.Event != "post_tool" || ev.State != "working" || ev.ToolOutput != "package main" {
			t.Errorf("tool completed part: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"message.part.updated","properties":{"part":{"type":"tool","sessionID":"ses_root","messageID":"msg_a1","tool":"bash","state":{"status":"error","error":"exit status 1"}}}}`)
		if ev.Event != "tool_failure" || ev.State != "working" || !strings.Contains(ev.Text, "exit status 1") {
			t.Errorf("tool error part: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"message.part.updated","properties":{"part":{"type":"text","sessionID":"ses_root","messageID":"msg_a1","text":"All tests pass."}}}`)
		if ev.Event != "response" || ev.Reply != "All tests pass." {
			t.Errorf("text part: %+v", ev)
		}

		ev = mustMapOne(t, `{"type":"message.part.updated","properties":{"part":{"type":"subtask","id":"sub_1","sessionID":"ses_root","description":"Explore codebase","status":"running"}}}`)
		if ev.Event != "subagent_start" || ev.Child == nil || ev.Child.ID != "sub_1" || ev.Child.State != "working" {
			t.Errorf("subtask part: %+v", ev)
		}
	})

	t.Run("global event wrapper and session.deleted", func(t *testing.T) {
		ev := mustMapOne(t, `{"directory":"/repo/global","payload":{"type":"session.deleted","properties":{"info":{"id":"ses_root"}}}}`)
		if ev.Event != "session_end" || ev.State != "idle" || ev.CWD != "/repo/global" {
			t.Errorf("global session.deleted: %+v", ev)
		}
	})
}

func TestOpenCodeEventMapper_ChildSubagentDoesNotClobberParent(t *testing.T) {
	mapper := NewOpenCodeEventMapper(agent.DaemonWatchTarget{CWD: "/repo"})
	now := int64(1700000000000)
	mapper.nowFn = func() int64 {
		now += 1000
		return now
	}

	// Root session starts and becomes busy.
	evs := mapper.Map("", `{"type":"session.created","properties":{"info":{"id":"ses_parent","title":"Parent task"}}}`)
	if len(evs) != 1 || evs[0].SessionID != "ses_parent" {
		t.Fatalf("parent created: %+v", evs)
	}
	evs = mapper.Map("", `{"type":"session.busy","properties":{"sessionID":"ses_parent"}}`)
	if len(evs) != 1 || evs[0].State != "working" {
		t.Fatalf("parent busy: %+v", evs)
	}

	// Child session is created with parentID.
	evs = mapper.Map("", `{"type":"session.created","properties":{"info":{"id":"ses_child","parentID":"ses_parent","title":"Audit tests"}}}`)
	if len(evs) != 1 {
		t.Fatalf("child created len=%d", len(evs))
	}
	if evs[0].Event != "subagent_start" || evs[0].SessionID != "ses_parent" || evs[0].Child == nil || evs[0].Child.ID != "ses_child" || evs[0].Child.State != "working" {
		t.Fatalf("unexpected child created event: %+v", evs[0])
	}

	// Child becomes busy -> Child update, root SessionID preserved, State empty (no clobber).
	evs = mapper.Map("", `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"busy"}}}`)
	if len(evs) != 1 || evs[0].SessionID != "ses_parent" || evs[0].State != "" || evs[0].Child == nil || evs[0].Child.State != "working" {
		t.Fatalf("unexpected child busy event: %+v", evs)
	}

	// Child becomes idle -> Child done, root pane NOT marked done!
	evs = mapper.Map("", `{"type":"session.idle","properties":{"sessionID":"ses_child"}}`)
	if len(evs) != 1 {
		t.Fatalf("child idle len=%d", len(evs))
	}
	if evs[0].Event != "subagent_stop" || evs[0].SessionID != "ses_parent" || evs[0].State != "" {
		t.Fatalf("child idle should not clobber parent state/session: %+v", evs[0])
	}
	if evs[0].Child == nil || evs[0].Child.ID != "ses_child" || evs[0].Child.State != "done" || evs[0].Child.Outcome != "done" || evs[0].Child.EndedMs == 0 {
		t.Fatalf("unexpected child idle Child payload: %+v", evs[0].Child)
	}

	// Child errors -> Child failed, root pane NOT marked failed!
	evs = mapper.Map("", `{"type":"session.error","properties":{"sessionID":"ses_child","error":"subagent boom"}}`)
	if len(evs) != 1 || evs[0].State != "" || evs[0].SessionID != "ses_parent" || evs[0].Child == nil || evs[0].Child.State != "failed" || evs[0].Child.Outcome != "failed" {
		t.Fatalf("unexpected child error event: %+v", evs)
	}

	// Parent finally finishes -> root pane marked done.
	evs = mapper.Map("", `{"type":"session.idle","properties":{"sessionID":"ses_parent"}}`)
	if len(evs) != 1 || evs[0].Event != "stop" || evs[0].State != "done" || evs[0].SessionID != "ses_parent" || evs[0].Child != nil {
		t.Fatalf("unexpected parent idle event: %+v", evs)
	}
}

func TestOpenCodeDaemonWatcher_StreamReconnectFallbackAndPIDExit(t *testing.T) {
	var connCount atomic.Int32
	var fallbackHit atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/event" {
			// Force fallback to /global/event on 404.
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/global/event" {
			http.NotFound(w, r)
			return
		}
		fallbackHit.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		attempt := connCount.Add(1)
		if attempt == 1 {
			// Emit one event, then drop the connection to trigger reconnect backoff.
			fmt.Fprint(w, "data: {\"directory\":\"/proj\",\"payload\":{\"type\":\"session.busy\",\"properties\":{\"sessionID\":\"ses_1\"}}}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}

		// Second connection: emit permission event and hold open until client disconnects.
		fmt.Fprint(w, "data: {\"directory\":\"/proj\",\"payload\":{\"type\":\"permission.asked\",\"properties\":{\"sessionID\":\"ses_1\",\"permission\":\"bash\",\"patterns\":[\"git push\"]}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	var pidAlive atomic.Bool
	pidAlive.Store(true)

	watcher := &OpenCodeDaemonWatcher{
		HTTPClient:       srv.Client(),
		BackoffInitial:   15 * time.Millisecond,
		BackoffMax:       50 * time.Millisecond,
		PIDCheckInterval: 20 * time.Millisecond,
		PIDAliveFn: func(pid int) bool {
			return pidAlive.Load()
		},
	}

	var (
		mu     sync.Mutex
		events []agent.DaemonEvent
	)
	gotSecond := make(chan struct{}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- watcher.Watch(ctx, agent.DaemonWatchTarget{
			Session:  "dev",
			PaneID:   "%1",
			PanePID:  4242,
			Endpoint: srv.URL,
		}, func(ev agent.DaemonEvent) {
			mu.Lock()
			events = append(events, ev)
			n := len(events)
			mu.Unlock()
			if n >= 2 {
				select {
				case gotSecond <- struct{}{}:
				default:
				}
			}
		})
	}()

	select {
	case <-gotSecond:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for reconnected SSE events")
	}

	// Simulate pane PID exiting -> Watch should tear down cleanly with nil error.
	pidAlive.Store(false)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch returned error on PID exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Watch did not exit cleanly after PIDAliveFn returned false")
	}

	if !fallbackHit.Load() {
		t.Errorf("expected 404 fallback to /global/event")
	}
	if connCount.Load() < 2 {
		t.Errorf("expected at least 2 connections (reconnect), got %d", connCount.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(events))
	}
	if events[0].State != "working" || events[0].SessionID != "ses_1" {
		t.Errorf("events[0] = %+v, want working/ses_1", events[0])
	}
	if events[1].State != "blocked" || events[1].Kind != "permission" || !strings.Contains(events[1].Text, "git push") {
		t.Errorf("events[1] = %+v, want blocked/permission/git push", events[1])
	}
}

func TestOpenCodeDiscoveryParsers(t *testing.T) {
	t.Run("cmdline", func(t *testing.T) {
		cases := []struct {
			argv []string
			want string
			ok   bool
		}{
			{[]string{"opencode", "--server", "http://127.0.0.1:4096/"}, "http://127.0.0.1:4096", true},
			{[]string{"opencode", "--server=http://localhost:4097"}, "http://localhost:4097", true},
			{[]string{"opencode", "--standalone", "--port", "4098"}, "http://127.0.0.1:4098", true},
			{[]string{"opencode", "--port=4099", "--hostname=127.0.0.1"}, "http://127.0.0.1:4099", true},
			{[]string{"opencode", "--port", "4100", "--hostname", "::1"}, "http://[::1]:4100", true},
			{[]string{"opencode", "--standalone"}, "", false},
		}
		for _, tc := range cases {
			got, ok := extractOpenCodeEndpointFromCmdline(tc.argv)
			if ok != tc.ok || got != tc.want {
				t.Errorf("extractOpenCodeEndpointFromCmdline(%v) = (%q, %v), want (%q, %v)", tc.argv, got, ok, tc.want, tc.ok)
			}
		}
	})

	t.Run("environ", func(t *testing.T) {
		env := []byte("PATH=/usr/bin\x00OPENCODE_SERVER_URL=http://127.0.0.1:4567/\x00HOME=/home/u\x00")
		got, ok := extractOpenCodeEndpointFromEnv(env)
		if !ok || got != "http://127.0.0.1:4567" {
			t.Errorf("extractOpenCodeEndpointFromEnv = (%q, %v), want (http://127.0.0.1:4567, true)", got, ok)
		}
		if _, ok := extractOpenCodeEndpointFromEnv([]byte("FOO=bar\x00")); ok {
			t.Errorf("expected false when OPENCODE_SERVER_URL is absent")
		}
	})

	t.Run("proc net tcp", func(t *testing.T) {
		// 0x1000 = 4096, 0x1001 = 4097, 0x1002 = 4098
		sampleV4 := strings.Join([]string{
			"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
			"   0: 0100007F:1000 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 00000000 100 0 0 10 0",
			"   1: 00000000:1001 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 23456 1 00000000 100 0 0 10 0",
			"   2: 0100007F:1002 0100007F:9999 01 00000000:00000000 00:00000000 00000000  1000        0 34567 1 00000000 100 0 0 10 0",
			"   3: 0501A8C0:1003 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 45678 1 00000000 100 0 0 10 0",
		}, "\n")
		m4 := parseProcNetTCP([]byte(sampleV4), false)
		if m4["12345"] != "http://127.0.0.1:4096" {
			t.Errorf("v4 loopback inode 12345 = %q", m4["12345"])
		}
		if m4["23456"] != "http://127.0.0.1:4097" {
			t.Errorf("v4 wildcard inode 23456 = %q", m4["23456"])
		}
		if _, ok := m4["34567"]; ok {
			t.Errorf("expected established socket (01) to be ignored")
		}
		if _, ok := m4["45678"]; ok {
			t.Errorf("expected non-loopback LAN socket to be ignored")
		}
	})

	t.Run("proc net tcp6", func(t *testing.T) {
		sampleV6 := strings.Join([]string{
			"  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
			"   0: 00000000000000000000000001000000:1004 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 55555 1 00000000",
			"   1: 00000000000000000000000000000000:1005 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 66666 1 00000000",
		}, "\n")
		m6 := parseProcNetTCP([]byte(sampleV6), true)
		if m6["55555"] != "http://[::1]:4100" {
			t.Errorf("v6 ::1 inode 55555 = %q", m6["55555"])
		}
		if m6["66666"] != "http://127.0.0.1:4101" {
			t.Errorf("v6 :: inode 66666 = %q", m6["66666"])
		}
	})
}

func TestRegistryDaemonWatcher(t *testing.T) {
	reg := agent.DefaultRegistry()

	if dw := reg.DaemonWatcher("opencode"); dw == nil {
		t.Fatalf("expected non-nil DaemonWatcher for opencode")
	}
	if dw := reg.DaemonWatcher("oc"); dw == nil {
		t.Fatalf("expected non-nil DaemonWatcher for alias oc")
	}
	for _, other := range []string{"claude", "codex", "antigravity", "gemini", "cursor", "unknown-agent"} {
		if dw := reg.DaemonWatcher(other); dw != nil {
			t.Errorf("expected nil DaemonWatcher for %q, got %T", other, dw)
		}
	}

	// Verify DaemonUsage JSON tags with omitempty.
	raw, err := json.Marshal(agent.DaemonUsage{InputTokens: 10, CostUSD: 0.01})
	if err != nil {
		t.Fatalf("marshal DaemonUsage: %v", err)
	}
	if string(raw) != `{"input_tokens":10,"cost_usd":0.01}` {
		t.Errorf("DaemonUsage JSON = %s, want {\"input_tokens\":10,\"cost_usd\":0.01}", raw)
	}
}
