package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookSend(t *testing.T) {
	var gotBody webhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	hook := NewWebhook(server.URL)
	if err := hook.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotBody.Content != "hello" {
		t.Errorf("content = %q, want %q", gotBody.Content, "hello")
	}
	if gotBody.AllowedMentions.Parse == nil || len(gotBody.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty non-nil slice (suppress all mentions)", gotBody.AllowedMentions.Parse)
	}
}

func TestWebhookSendNilIsNoop(t *testing.T) {
	var hook *Webhook
	if err := hook.Send(context.Background(), "hello"); err != nil {
		t.Errorf("nil webhook Send should no-op, got %v", err)
	}
}

func TestNewWebhookEmptyURL(t *testing.T) {
	if NewWebhook("") != nil {
		t.Error("NewWebhook(\"\") should return nil")
	}
}

func TestWebhookSendErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	hook := NewWebhook(server.URL)
	if err := hook.Send(context.Background(), "hello"); err == nil {
		t.Error("expected error on non-2xx response")
	}
}
