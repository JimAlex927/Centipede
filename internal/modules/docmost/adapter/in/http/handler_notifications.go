package http

import (
	"context"
	"encoding/json"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"
)

func (handler *Handler) publishCommentNotifications(deliveries []postgres.CommentNotificationDelivery, workspaceID string) {
	for _, delivery := range deliveries {
		if handler.realtime != nil {
			handler.realtime.PublishNotification(workspaceID, delivery.UserID, map[string]any{
				"type": delivery.Type, "notificationId": delivery.ID,
			})
		}
		handler.enqueueNotificationEmail(delivery.ID)
	}
}

func (handler *Handler) notifyPageVerification(ctx context.Context, page domain.Page, actorID, notificationType string, eventKey time.Time) {
	key := eventKey.UTC().Format(time.RFC3339Nano)
	dataValue := map[string]string{"eventKey": key}
	if notificationType == "page.verification_expiring" || notificationType == "page.verification_expired" {
		dataValue["expiresAt"] = key
	}
	data, err := json.Marshal(dataValue)
	if err != nil {
		return
	}
	deliveries, err := handler.repository.CreatePageVerificationNotifications(
		ctx, page.ID, page.WorkspaceID, actorID, notificationType, key, data,
	)
	if err != nil {
		return
	}
	for _, delivery := range deliveries {
		if handler.realtime != nil {
			handler.realtime.PublishNotification(page.WorkspaceID, delivery.UserID, map[string]any{
				"type": notificationType, "notificationId": delivery.ID, "pageId": page.ID,
			})
		}
		if notificationType != "page.verified" {
			handler.enqueueNotificationEmail(delivery.ID)
		}
	}
}

func (handler *Handler) notifyPagePermissionGranted(ctx context.Context, page domain.Page, actorID, role string, userIDs, groupIDs []string, eventKey time.Time) {
	deliveries, err := handler.repository.CreatePagePermissionNotifications(
		ctx, page.ID, page.WorkspaceID, page.SpaceID, actorID, role,
		eventKey.UTC().Format(time.RFC3339Nano), userIDs, groupIDs,
	)
	if err != nil {
		return
	}
	for _, delivery := range deliveries {
		if handler.realtime != nil {
			handler.realtime.PublishNotification(page.WorkspaceID, delivery.UserID, map[string]any{
				"type": "page.permission_granted", "notificationId": delivery.ID, "pageId": page.ID,
			})
		}
		handler.enqueueNotificationEmail(delivery.ID)
	}
}

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
	for _, delivery := range mentionDeliveries {
		handler.enqueueNotificationEmail(delivery.ID)
	}
	deliveries, err := handler.repository.CreatePageUpdateNotifications(
		ctx, page.ID, page.WorkspaceID, actorID, []string{actorID},
	)
	if err != nil {
		return
	}
	for _, delivery := range deliveries {
		if handler.realtime != nil {
			handler.realtime.PublishNotification(page.WorkspaceID, delivery.UserID, map[string]any{
				"type":           "page.updated",
				"notificationId": delivery.ID,
				"pageId":         page.ID,
			})
		}
		handler.enqueueNotificationEmail(delivery.ID)
	}
}
