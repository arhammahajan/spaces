package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	hookcore "github.com/arhammahajan/spaces/internal/hooks/core"
	"github.com/arhammahajan/spaces/internal/model"
)

func TestOpenCodeSSEEndToEndPluginToModel(t *testing.T) {
	t.Setenv("SPACES_HOOKS_DIR", t.TempDir())
	bindHookSeams()

	if hookcore.DaemonWatchResolver == nil {
		t.Fatal("expected bindHookSeams to bind DaemonWatchResolver")
	}
	if fn := hookcore.DaemonWatchResolver("claude"); fn != nil {
		t.Fatalf("expected nil DaemonWatchFn for claude, got non-nil")
	}
	watchFn := hookcore.DaemonWatchResolver("opencode")
	if watchFn == nil {
		t.Fatal("expected non-nil DaemonWatchFn for opencode")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/event" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`{"type":"session.created","properties":{"info":{"id":"ses_root","title":"Refactor SSE","directory":"/repo"}}}`,
			`{"type":"session.busy","properties":{"sessionID":"ses_root"}}`,
			`{"type":"tool.execute.before","properties":{"sessionID":"ses_root","tool":"bash","input":{"command":"go test ./..."}}}`,
			`{"type":"message.updated","properties":{"info":{"role":"assistant","sessionID":"ses_root","providerID":"anthropic","modelID":"claude-sonnet-4-5","tokens":{"input":12400,"output":3100,"cache":{"read":8200,"write":400}},"cost":0.042}}}`,
		}
		for _, payload := range events {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if flusher != nil {
				flusher.Flush()
			}
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	mgr := hookcore.NewDaemonWatchManager(
		func(ctx context.Context) ([]hookcore.DaemonPaneTarget, error) {
			return []hookcore.DaemonPaneTarget{{
				Session:       "dev",
				PaneID:        "%7",
				PaneNumericID: "7",
				PanePID:       os.Getpid(),
				Agent:         "opencode",
				CWD:           "/repo",
				Endpoint:      srv.URL,
			}}, nil
		},
		hookcore.DaemonWatchResolver,
		hookcore.MergeIntoStore,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.Run(ctx, 50*time.Millisecond)
	}()

	deadline := time.Now().Add(3 * time.Second)
	var snap model.HookSnapshot
	var got model.HookSummary
	for time.Now().Before(deadline) {
		var err error
		snap, err = model.FetchHooks(context.Background())
		if err == nil {
			if pm, ok := snap.Panes["dev"]; ok {
				if rec, ok := pm["7"]; ok && rec.Usage != nil && rec.Usage.InputTokens == 12400 {
					got = rec
					break
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	<-done

	if got.Source != "daemon_sse" {
		t.Fatalf("Source = %q, want daemon_sse", got.Source)
	}
	if got.Agent != "opencode" {
		t.Fatalf("Agent = %q, want opencode", got.Agent)
	}
	if got.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("Model = %q, want anthropic/claude-sonnet-4-5", got.Model)
	}
	if got.Title != "Refactor SSE" {
		t.Fatalf("Title = %q, want Refactor SSE", got.Title)
	}
	if got.Usage == nil || got.Usage.InputTokens != 12400 || got.Usage.OutputTokens != 3100 || got.Usage.CacheReadTokens != 8200 {
		t.Fatalf("unexpected Usage: %+v", got.Usage)
	}
}
