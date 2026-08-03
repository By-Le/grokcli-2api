package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hm2899/grokcli-2api/internal/upstream/grok"
)

func TestClientOpenUsesChatCompletions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization=%q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != DefaultModel || payload["stream"] != true || payload["reasoning_split"] != true {
			t.Fatalf("payload=%#v", payload)
		}
		if payload["max_completion_tokens"] != float64(64) || payload["max_tokens"] != nil {
			t.Fatalf("token fields=%#v", payload)
		}
		if payload["prompt_cache_key"] != nil {
			t.Fatalf("prompt_cache_key should be local-only: %#v", payload)
		}
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "adaptive" {
			t.Fatalf("thinking=%#v", thinking)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	}))
	defer server.Close()

	client := &Client{OpenAIBaseURL: server.URL + "/v1", HTTP: server.Client()}
	response, err := client.Open(context.Background(), grok.Account{ID: "minimax", Token: "test-token"}, DefaultModel, map[string]any{
		"messages":         []any{map[string]any{"role": "user", "content": "hello"}},
		"max_tokens":       64,
		"reasoning_effort": "high",
		"prompt_cache_key": "local-cache-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"content":"ok"`) {
		t.Fatalf("body=%s", body)
	}
}

func TestClientOpenPreservesReasoningSplit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["reasoning_split"] != false {
			t.Fatalf("reasoning_split=%#v", payload["reasoning_split"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := &Client{OpenAIBaseURL: server.URL + "/v1", HTTP: server.Client()}
	response, err := client.Open(context.Background(), grok.Account{Token: "test-token"}, DefaultModel, map[string]any{
		"messages":        []any{map[string]any{"role": "user", "content": "hello"}},
		"reasoning_split": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
}

func TestClientOpenMessagesUsesAnthropicEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("anthropic-beta") != "prompt-caching" {
			t.Fatalf("headers=%v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != ModelM27 {
			t.Fatalf("model=%#v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","model":"MiniMax-M2.7","content":[{"type":"text","text":"ok"}]}`)
	}))
	defer server.Close()

	client := &Client{AnthropicBaseURL: server.URL + "/anthropic", HTTP: server.Client()}
	headers := http.Header{}
	headers.Set("anthropic-beta", "prompt-caching")
	response, err := client.OpenMessages(context.Background(), grok.Account{ID: "minimax", Token: "test-token"}, ModelM27, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
}

func TestEndpointsCatalogAndAliases(t *testing.T) {
	endpoints, ok := EndpointsForRegion(RegionCNZH)
	if !ok {
		t.Fatal("CN region missing")
	}
	if endpoints.OpenAIBaseURL != "https://api.minimaxi.com/v1" || endpoints.AnthropicBaseURL != "https://api.minimaxi.com/anthropic" {
		t.Fatalf("endpoints=%#v", endpoints)
	}
	if ResolveModel("minimax-m2.7", DefaultModel) != ModelM27 || ResolveModel("minimax-latest", DefaultModel) != DefaultModel {
		t.Fatal("model aliases did not resolve")
	}
	entries := CatalogEntries(123)
	if len(entries) != 2 {
		t.Fatalf("entries=%#v", entries)
	}
	if entries[0]["context_window"] != 1_000_000 {
		t.Fatalf("first entry=%#v", entries[0])
	}
	pricing, _ := entries[1]["pricing_usd_per_million_tokens"].(map[string]any)
	if pricing["cache_write"] != 0.375 {
		t.Fatalf("pricing=%#v", pricing)
	}
}
