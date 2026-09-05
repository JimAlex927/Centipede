package http

import (
	"context"

	"centipede/internal/modules/docmost/domain"
)

// notifyPageUpdated mirrors the in-app part of Docmost's page update worker for
// mutations that arrive through the REST API instead of collaboration.
func (handler *Handler) notifyPageUpdated(ctx context.Context, page domain.Page, actorID string) {
	mentionDeliveries, _ := handler.repository.CreatePageMentionNotifications(ctx, page.ID, page.WorkspaceID, actorID, page.Content)
	if handler.realtime != nil {
		for _, delivery := range mentionDeliveries {
			handler.realtime.PublishNotification(page.WorkspaceID, delivery.UserID, map[string]any{
				"type": "page.user_mention", "notificationId": delivery.ID, "pageId": page.ID,
			})
		}
	}
	deliveries, err := handler.repository.CreatePageUpdateNotifications(
		ctx, page.ID, page.WorkspaceID, actorID, []string{actorID},
	)
	if err != nil || handler.realtime == nil {
		return
	}
	for _, delivery := range deliveries {
		handler.realtime.PublishNotification(page.WorkspaceID, delivery.UserID, map[string]any{
			"type":           "page.updated",
			"notificationId": delivery.ID,
			"pageId":         page.ID,
		})
	}
}
