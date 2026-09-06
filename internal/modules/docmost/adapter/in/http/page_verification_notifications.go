package http

import (
	"context"
	"time"

	"centipede/internal/modules/docmost/domain"
)

const pageVerificationNotificationInterval = time.Hour

// startPageVerificationNotifications replaces the enterprise verification
// reminder queue. PostgreSQL is the source of truth and notification inserts
// are idempotent, allowing the scan to recover after a restart.
func (handler *Handler) startPageVerificationNotifications() {
	if handler.repository == nil {
		return
	}
	go func() {
		startupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		handler.processPageVerificationNotifications(startupContext)
		cancel()

		ticker := time.NewTicker(pageVerificationNotificationInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			handler.processPageVerificationNotifications(ctx)
			cancel()
		}
	}()
}

func (handler *Handler) processPageVerificationNotifications(ctx context.Context) {
	candidates, err := handler.repository.PendingPageVerificationExpiryNotifications(ctx, time.Now().UTC(), 100)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		if !handler.workspaceHasFeature(ctx, candidate.WorkspaceID, verificationFeature) {
			continue
		}
		handler.notifyPageVerification(
			ctx,
			domain.Page{ID: candidate.PageID, WorkspaceID: candidate.WorkspaceID},
			"",
			candidate.NotificationType,
			candidate.ExpiresAt,
		)
	}
}
