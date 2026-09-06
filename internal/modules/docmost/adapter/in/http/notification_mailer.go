package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
)

const notificationMailPollInterval = 30 * time.Second

// startNotificationMailWorker replaces the upstream BullMQ notification
// email queue. Notifications are already durable in PostgreSQL; the channel
// makes new messages prompt, while the periodic scan recovers messages after
// a process restart or a transient SMTP failure.
func (handler *Handler) startNotificationMailWorker() {
	if handler.repository == nil || handler.mailer == nil || !handler.mailer.Enabled() {
		return
	}
	handler.notificationMailQueue = make(chan string, 256)
	go func() {
		ticker := time.NewTicker(notificationMailPollInterval)
		defer ticker.Stop()
		for {
			select {
			case notificationID := <-handler.notificationMailQueue:
				handler.sendNotificationEmail(notificationID)
			case <-ticker.C:
				handler.sendPendingNotificationEmails()
			}
		}
	}()
}

func (handler *Handler) enqueueNotificationEmail(notificationID string) {
	if handler.notificationMailQueue == nil || notificationID == "" {
		return
	}
	select {
	case handler.notificationMailQueue <- notificationID:
	default:
		// The next periodic scan will recover an item if the prompt queue is full.
	}
}

func (handler *Handler) sendPendingNotificationEmails() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	items, err := handler.repository.PendingCommentNotificationEmails(ctx, 100)
	if err != nil {
		return
	}
	for _, item := range items {
		handler.sendNotificationEmailData(ctx, item)
	}
}

func (handler *Handler) sendNotificationEmail(notificationID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	item, err := handler.repository.NotificationEmailByID(ctx, notificationID)
	if err != nil || item == nil {
		return
	}
	handler.sendNotificationEmailData(ctx, *item)
}

func (handler *Handler) sendNotificationEmailData(ctx context.Context, item postgres.NotificationEmail) {
	subject := "Docmost notification"
	switch item.Type {
	case "comment.user_mention":
		subject = fmt.Sprintf("%s mentioned you in a comment", item.ActorName)
	case "comment.created":
		subject = fmt.Sprintf("%s commented on %s", item.ActorName, item.PageTitle)
	case "comment.resolved":
		subject = fmt.Sprintf("%s resolved a comment on %s", item.ActorName, item.PageTitle)
	case "page.user_mention":
		subject = fmt.Sprintf("%s mentioned you on %s", item.ActorName, item.PageTitle)
	case "page.permission_granted":
		role := "reader"
		var data struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(item.Data, &data) == nil && data.Role == "writer" {
			role = "writer"
		}
		access := "view"
		if role == "writer" {
			access = "edit"
		}
		subject = fmt.Sprintf("%s gave you %s access to %s", item.ActorName, access, item.PageTitle)
	case "page.updated":
		subject = fmt.Sprintf("%s updated %s", item.ActorName, item.PageTitle)
	case "page.verified":
		subject = fmt.Sprintf("%s verified %s", item.ActorName, item.PageTitle)
	case "page.approval_requested":
		subject = fmt.Sprintf("%s submitted %s for approval", item.ActorName, item.PageTitle)
	case "page.approval_rejected":
		subject = fmt.Sprintf("%s returned %s for revision", item.ActorName, item.PageTitle)
	case "page.verification_expiring":
		subject = fmt.Sprintf("Verification expires soon: %s", item.PageTitle)
	case "page.verification_expired":
		subject = fmt.Sprintf("Page verification expired: %s", item.PageTitle)
	}
	body := fmt.Sprintf("%s\n\n", subject)
	if handler.frontendURL != "" && item.SpaceSlug != "" && item.PageSlugID != "" {
		body += "Open the page: " + strings.TrimRight(handler.frontendURL, "/") + "/s/" + url.PathEscape(item.SpaceSlug) + "/p/" + url.PathEscape(item.PageSlugID) + "\n"
	}
	if err := handler.mailer.Send(ctx, item.Email, subject, body); err != nil {
		return
	}
	_ = handler.repository.MarkNotificationEmailed(ctx, item.ID)
}
