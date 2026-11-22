package modelpick

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/arhammahajan/spaces/internal/harness/harnessenv"
)

// fixtureModels is the canned GET /v1/models body, written from the
// documented live shape: metadata.router carrying limits with
// {kind,source,value} facts, nulls in the sibling limit fields, a
// reasoning effort enum, modalities input facts, and
// freshness.discovery.stale. The null rows exercise every absence the
// live payload serves: value null, kind unknown, whole keys missing,
// and null metadata.
const fixtureModels = `{
  "object": "list",
  "data": [
    {
      "id": "glm-5.3-flash",
      "object": "model",
      "owned_by": "model-router",
      "metadata": {
        "label": "fixture",
        "router": {
          "schema_version": 1,
          "sources": {
            "go.review": {"verification": "reviewed facts", "stale": false}
          },
          "limits": {
            "context_window": {"value": 1000000, "source": "go.review.limits.context_window", "kind": "reviewed"},
            "max_output_tokens": {"value": 131072, "source": "go.review.limits.max_output_tokens", "kind": "reviewed"},
            "trained_context_window": {"value": null, "source": null, "kind": "unknown"},
            "max_input_tokens": {"value": null, "source": null, "kind": "unknown"}
          },
          "reasoning": {
            "supported": {"value": true, "source": "go.review.reasoning.supported", "kind": "reviewed"},
            "effort": {
              "supported": {"value": true, "source": null, "kind": "reviewed"},
              "values": {"value": ["low", "high", "max"], "source": "go.review.reasoning.effort.values", "kind": "reviewed"},
              "native_parameter": {"value": {"protocol": "openai-chat", "path": "reasoning_effort"}, "source": null, "kind": "reviewed"}
            }
          },
          "modalities": {
            "input": {
              "text": {"value": true, "source": "go.review.modalities.input.text", "kind": "reviewed"},
              "image": {"value": true, "source": "go.review.modalities.input.image", "kind": "reviewed"},
              "audio": {"value": false, "source": "go.review.modalities.input.audio", "kind": "reviewed"},
              "video": {"value": null, "source": null, "kind": "unknown"},
              "pdf": {"value": null, "source": null, "kind": "unknown"},
              "embedding": {"value": null, "source": null, "kind": "unknown"}
            },
            "output": {
              "text": {"value": true, "source": null, "kind": "reviewed"},
              "image": {"value": false, "source": null, "kind": "reviewed"},
              "audio": {"value": false, "source": null, "kind": "reviewed"},
              "video": {"value": false, "source": null, "kind": "reviewed"},
              "pdf": {"value": false, "source": null, "kind": "reviewed"},
              "embedding": {"value": false, "source": null, "kind": "reviewed"}
            }
          },
          "freshness": {
            "discovery": {
              "stale": false,
              "age_seconds": 3.2,
              "last_success": 1792957510.5,
              "last_attempt": 1792957510.5,
              "next_attempt": null,
              "consecutive_failures": 0,
              "stale_age_seconds": 0,
              "cached": false,
              "succeeded_at": 1792957510.5
            }
          }
        }
      }
    },
    {
      "id": "openai/gpt-5-mini",
      "object": "model",
      "owned_by": "model-router",
      "metadata": {
        "router": {
          "limits": {
            "context_window": {"value": null, "source": null, "kind": "unknown"},
            "max_output_tokens": {"value": null, "source": null, "kind": "unknown"}
          },
          "reasoning": {
            "supported": {"value": true, "source": "go.review.reasoning.supported", "kind": "reviewed"},
            "effort": {
              "values": {"value": null, "source": null, "kind": "unknown"}
            }
          },
          "modalities": {
            "input": {
              "text": {"value": true, "source": "go.review.modalities.input.text", "kind": "reviewed"},
              "image": {"value": null, "source": null, "kind": "unknown"}
            }
          },
          "freshness": {
            "discovery": {
              "stale": true,
              "age_seconds": null,
              "last_success": null,
              "last_attempt": 1792957600.1,
              "next_attempt": 1792957605.1,
              "consecutive_failures": 2,
              "stale_age_seconds": null,
              "cached": false,
              "succeeded_at": null
            }
          }
        }
      }
    },
    {
      "id": "bare/row-without-metadata",
      "object": "model",
      "owned_by": "model-router"
    },
    {
      "id": "nulls/everywhere",
      "object": "model",
      "owned_by": "model-router",
      "metadata": null
    },
    {
      "id": "label-only/row",
      "object": "model",
      "owned_by": "model-router",
      "metadata": {"label": "no router facts", "router": null}
    },
    {
      "id": "",
      "object": "model",
      "owned_by": "model-router"
    }
  ]
}`

func TestFetchParsesLiveShapes(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixtureModels)
	}))
	defer srv.Close()

	models, err := Fetch(harnessenv.RouterSettings{URL: srv.URL, APIKey: "test-key"}, 0)
	if err != nil {
		t.Fatalf("fetch fixture: %v", err)
	}
	want := []Model{
		{
			ID:            "glm-5.3-flash",
			OwnedBy:       "model-router",
			ContextWindow: 1000000,
			MaxOutput:     131072,
			Reasoning:     "low,high,max",
			Modalities:    []string{"text", "image"},
		},
		{
			ID:         "openai/gpt-5-mini",
			OwnedBy:    "model-router",
			Reasoning:  "supported",
			Modalities: []string{"text"},
			Stale:      true,
		},
		{ID: "bare/row-without-metadata", OwnedBy: "model-router"},
		{ID: "nulls/everywhere", OwnedBy: "model-router"},
		{ID: "label-only/row", OwnedBy: "model-router"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("parsed listing:\n got %#v\nwant %#v", models, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 1 || auths[0] != "Bearer test-key" {
		t.Fatalf("bearer auth not sent: %v", auths)
	}
}

func TestFetchOmitsAuthWithoutKey(t *testing.T) {
	var mu sync.Mutex
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		fmt.Fprint(w, `{"data": [{"id": "a/b"}]}`)
	}))
	defer srv.Close()

	if _, err := Fetch(harnessenv.RouterSettings{URL: srv.URL}, 0); err != nil {
		t.Fatalf("fetch without key: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "" {
		t.Fatalf("authorization header must be omitted, got %q", auth)
	}
}

func TestFetchToleratesBareArrayEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id": "x/y", "owned_by": "owner"}]`)
	}))
	defer srv.Close()

	models, err := Fetch(harnessenv.RouterSettings{URL: srv.URL, APIKey: "k"}, 0)
	if err != nil {
		t.Fatalf("bare array: %v", err)
	}
	want := []Model{{ID: "x/y", OwnedBy: "owner"}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("bare array listing: got %#v want %#v", models, want)
	}
}

func TestFetchErrors(t *testing.T) {
	t.Run("no URL", func(t *testing.T) {
		if _, err := Fetch(harnessenv.RouterSettings{}, 0); err == nil || !strings.Contains(err.Error(), "no router URL") {
			t.Fatalf("want no-URL error, got %v", err)
		}
	})
	t.Run("invalid URL", func(t *testing.T) {
		if _, err := Fetch(harnessenv.RouterSettings{URL: "://not-a-url"}, 0); err == nil || !strings.Contains(err.Error(), "invalid router URL") {
			t.Fatalf("want invalid-URL error, got %v", err)
		}
	})
	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		if _, err := Fetch(harnessenv.RouterSettings{URL: srv.URL}, 0); err == nil || !strings.Contains(err.Error(), "status 503") {
			t.Fatalf("want status error, got %v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()
		if _, err := Fetch(harnessenv.RouterSettings{URL: url}, 0); err == nil || !strings.Contains(err.Error(), "/v1/models") {
			t.Fatalf("want transport error naming the endpoint, got %v", err)
		}
	})
	t.Run("malformed body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not-json")
		}))
		defer srv.Close()
		if _, err := Fetch(harnessenv.RouterSettings{URL: srv.URL}, 0); err == nil || !strings.Contains(err.Error(), "parse models listing") {
			t.Fatalf("want parse error, got %v", err)
		}
	})
}

func TestParseTolerance(t *testing.T) {
	body := `{"data": [
		{"id": "a/string-value", "owned_by": "o", "metadata": {"router": {"limits": {"context_window": {"value": "999", "kind": "override"}}}}},
		{"id": "b/fractional", "owned_by": "o", "metadata": {"router": {"limits": {"context_window": {"value": 1.5, "kind": "x"}, "max_output_tokens": {"value": -5, "kind": "x"}}}}},
		{"id": "c/huge-integer", "owned_by": "o", "metadata": {"router": {"limits": {"context_window": {"value": 9007199254740993, "kind": "override"}}}}},
		{"id": "d/mixed-effort-values", "owned_by": "o", "metadata": {"router": {"reasoning": {"effort": {"values": {"value": ["low", 3, true, "high"]}}}}}},
		{"id": "e/unknown-modality", "owned_by": "o", "metadata": {"router": {"modalities": {"input": {"text": {"value": true}, "hologram": {"value": true}, "audio": {"value": false}}}}}},
		{"id": "f/stale-not-a-bool", "owned_by": "o", "metadata": {"router": {"freshness": {"discovery": {"stale": "yes"}}}}},
		{"id": "g/null-blocks", "owned_by": "o", "metadata": {"router": {"limits": null, "reasoning": null, "modalities": null, "freshness": null}}},
		{"id": "h/metadata-not-an-object", "owned_by": "o", "metadata": "weird"}
	]}`
	models, err := parseModels([]byte(body))
	if err != nil {
		t.Fatalf("tolerant parse: %v", err)
	}
	byID := make(map[string]Model, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	if got := byID["a/string-value"].ContextWindow; got != 0 {
		t.Fatalf("string limit value must read as unknown, got %d", got)
	}
	if got := byID["b/fractional"].ContextWindow; got != 0 {
		t.Fatalf("fractional limit must read as unknown, got %d", got)
	}
	if got := byID["b/fractional"].MaxOutput; got != 0 {
		t.Fatalf("negative limit must read as unknown, got %d", got)
	}
	if got := byID["c/huge-integer"].ContextWindow; got != 9007199254740993 {
		t.Fatalf("huge integer must survive exactly, got %d", got)
	}
	if got := byID["d/mixed-effort-values"].Reasoning; got != "low,high" {
		t.Fatalf("non-string effort entries must be skipped, got %q", got)
	}
	if got := byID["e/unknown-modality"].Modalities; !reflect.DeepEqual(got, []string{"text", "hologram"}) {
		t.Fatalf("modalities must keep vocabulary order then sorted extras, got %v", got)
	}
	if byID["f/stale-not-a-bool"].Stale {
		t.Fatal("non-bool stale must read as not stale")
	}
	for _, id := range []string{"g/null-blocks", "h/metadata-not-an-object"} {
		m := byID[id]
		if m.ContextWindow != 0 || m.MaxOutput != 0 || m.Reasoning != "" || m.Modalities != nil || m.Stale {
			t.Fatalf("%s must parse fully unknown, got %#v", id, m)
		}
	}
}

func TestParseEmptyAndNullData(t *testing.T) {
	for name, body := range map[string]string{
		"empty envelope": `{"data": []}`,
		"null data":      `{"data": null}`,
	} {
		models, err := parseModels([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(models) != 0 {
			t.Fatalf("%s: want empty listing, got %#v", name, models)
		}
	}
}
