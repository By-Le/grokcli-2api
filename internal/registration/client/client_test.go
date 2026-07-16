package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartUsesVersionedInternalContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/registration/v1/jobs" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Idempotency-Key") != "idem" {
			t.Fatalf("headers=%v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "batch_id": "b"})
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, Token: "secret", HTTP: server.Client()}
	result, err := client.Start(context.Background(), map[string]any{"count": 2}, "idem")
	if err != nil || result["batch_id"] != "b" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
