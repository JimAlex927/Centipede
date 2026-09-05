package http

import (
	"errors"
	"net/http"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

type notificationRequest struct {
	NotificationIDs []string `json:"notificationIds"`
	Type            string   `json:"type"`
	Limit           int      `json:"limit"`
}

func (handler *Handler) notifications(c *gin.Context) {
	var request notificationRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Notifications(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Type, request.Limit)
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid notification type")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to load notifications")
		}
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) unreadNotificationCount(c *gin.Context) {
	current := currentPrincipal(c)
	count, err := handler.repository.UnreadNotificationCount(c.Request.Context(), current.Workspace.ID, current.User.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to count notifications")
		return
	}
	writeData(c, http.StatusOK, gin.H{"count": count})
}

func (handler *Handler) markNotificationsRead(c *gin.Context) {
	var request notificationRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if err := handler.repository.MarkNotificationsRead(c.Request.Context(), current.Workspace.ID, current.User.ID, request.NotificationIDs); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to mark notifications as read")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) markAllNotificationsRead(c *gin.Context) {
	current := currentPrincipal(c)
	if err := handler.repository.MarkAllNotificationsRead(c.Request.Context(), current.Workspace.ID, current.User.ID); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to mark notifications as read")
		return
	}
	writeData(c, http.StatusOK, nil)
}
