package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hootmeow/spawnpoint/domain"
	"github.com/hootmeow/spawnpoint/internal/storage/postgres"
)

// RoundSummaryStore is the subset of *postgres.Store this package needs,
// so callers can pass the real store without an import cycle back here.
type RoundSummaryStore interface {
	RoundNotificationSummary(ctx context.Context, roundID string) (postgres.RoundNotificationSummary, error)
}

// RoundComplete sends a "round complete" Discord message for each round in
// roundIDs that ended within window of now, and stays silent for anything
// older — the signal that suppresses backlog/reingest notification storms
// without needing a separate "is this historical" flag threaded through
// ingest. A nil webhook (not configured) is a no-op.
func RoundComplete(ctx context.Context, store RoundSummaryStore, webhook *Webhook, roundIDs []string, window time.Duration) {
	if webhook == nil || store == nil {
		return
	}
	for _, roundID := range roundIDs {
		summary, err := store.RoundNotificationSummary(ctx, roundID)
		if err != nil {
			log.Printf("round notify %s: %v", roundID, err)
			continue
		}
		if time.Since(summary.EndedAt) > window {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Round complete** on `%s`", summary.MapName)
		if summary.PlayerCount > 0 {
			fmt.Fprintf(&b, " — %d players", summary.PlayerCount)
		}
		if summary.TopFragName != "" {
			fmt.Fprintf(&b, "\nTop frag: **%s** (%d kills)", summary.TopFragName, summary.TopFrag)
		}
		for _, medal := range summary.Medals {
			fmt.Fprintf(&b, "\n%s: %s", domain.MedalLabel(medal.Category), medal.Player)
		}
		if err := webhook.Send(ctx, b.String()); err != nil {
			log.Printf("round notify %s: webhook: %v", roundID, err)
		}
	}
}
