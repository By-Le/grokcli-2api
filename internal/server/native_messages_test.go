package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hm2899/grokcli-2api/internal/config"
	"github.com/hm2899/grokcli-2api/internal/models"
	"github.com/hm2899/grokcli-2api/internal/pool"
	"github.com/hm2899/grokcli-2api/internal/upstream/minimax"
)

func TestMiniMaxMessagesUsesNativeEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer upstream.Close()

	cfg := config.Config{MiniMaxAPIKey: "configured", DefaultModel: minimax.DefaultModel}
	handler := NewMux(Options{
		Ready:           func() bool { return true },
		MessagesEnabled: true,
		Models:          models.NewCatalog(cfg, nil),
		Candidates: []pool.Candidate{{
			ID:      "minimax",
			Token:   "test-token",
			Enabled: true,
		}},
		Upstream: &minimax.Client{
			AnthropicBaseURL: upstream.URL + "/anthropic",
			HTTP:             upstream.Client(),
		},
		Config: cfg,
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"MiniMax-M3","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"text":"hello"`) || !strings.Contains(recorder.Body.String(), `"model":"MiniMax-M3"`) {
		t.Fatalf("body=%s", recorder.Body.String())
	}
}
