// Package notify posts plain-text messages to a Discord incoming webhook.
// Discord's webhook API accepts a JSON body of {"content": "..."}, which is
// all this package sends — other webhook receivers (e.g. Slack, which wants
// a "text" key instead) would need their own shim.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Webhook struct {
	URL    string
	Client *http.Client
}

type webhookPayload struct {
	Content         string                 `json:"content"`
	AllowedMentions webhookAllowedMentions `json:"allowed_mentions"`
}

type webhookAllowedMentions struct {
	// Parse is marshaled as an explicit [] (never omitted/null): Discord
	// treats a missing allowed_mentions as "parse everything," so this must
	// always serialize to an empty array to suppress mentions.
	Parse []string `json:"parse"`
}

func NewWebhook(url string) *Webhook {
	if url == "" {
		return nil
	}
	return &Webhook{URL: url, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Send posts message as-is; a nil receiver (no webhook configured) is a
// silent no-op so callers don't need to guard every call site.
//
// allowed_mentions is forced to an empty parse list because message text
// includes player display names pulled straight from game server logs — a
// player naming themselves "@everyone" or embedding a raw role/user mention
// token must not be able to make Discord actually ping anyone.
func (w *Webhook) Send(ctx context.Context, message string) error {
	if w == nil || w.URL == "" {
		return nil
	}
	body, err := json.Marshal(webhookPayload{
		Content:         message,
		AllowedMentions: webhookAllowedMentions{Parse: []string{}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("webhook post failed: %s: %s", resp.Status, string(respBody))
	}
	return nil
}
