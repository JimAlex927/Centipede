package http

import (
	"net/http"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"
	"github.com/gin-gonic/gin"
)

type auditRequest struct {
	Event        string `json:"event"`
	ResourceType string `json:"resourceType"`
	ActorID      string `json:"actorId"`
	SpaceID      string `json:"spaceId"`
	StartDate    string `json:"startDate"`
	EndDate      string `json:"endDate"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

func (handler *Handler) auditLogs(c *gin.Context) {
	if !handler.requireFeature(c, "audit:logs") {
		return
	}
	current := currentPrincipal(c)
	if !isOwner(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request auditRequest
	if !decodeOptional(c, &request) {
		return
	}
	start, err := parseAuditDate(request.StartDate, false)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid audit start date")
		return
	}
	end, err := parseAuditDate(request.EndDate, true)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid audit end date")
		return
	}
	result, err := handler.repository.AuditLogs(c.Request.Context(), postgres.AuditListInput{
		WorkspaceID: current.Workspace.ID, Event: strings.TrimSpace(request.Event), ResourceType: strings.TrimSpace(request.ResourceType),
		ActorID: strings.TrimSpace(request.ActorID), SpaceID: strings.TrimSpace(request.SpaceID), StartDate: start, EndDate: end,
		Cursor: request.Cursor, Limit: request.Limit,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load audit logs")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) auditRetention(c *gin.Context) {
	if !handler.requireFeature(c, "audit:logs") {
		return
	}
	current := currentPrincipal(c)
	if !isOwner(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	days, err := handler.repository.AuditRetentionDays(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load audit retention")
		return
	}
	writeData(c, http.StatusOK, gin.H{"retentionDays": days})
}

func (handler *Handler) updateAuditRetention(c *gin.Context) {
	if !handler.requireFeature(c, "audit:logs") {
		return
	}
	current := currentPrincipal(c)
	if !isOwner(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request struct {
		AuditRetentionDays int `json:"auditRetentionDays"`
	}
	if !decode(c, &request) {
		return
	}
	if request.AuditRetentionDays < 1 || request.AuditRetentionDays > 3650 {
		writeError(c, http.StatusBadRequest, "Audit retention must be between 1 and 3650 days")
		return
	}
	if err := handler.repository.UpdateAuditRetentionDays(c.Request.Context(), current.Workspace.ID, request.AuditRetentionDays); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to update audit retention")
		return
	}
	writeData(c, http.StatusOK, gin.H{"retentionDays": request.AuditRetentionDays})
}

func parseAuditDate(value string, endOfDay bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
		if err == nil && endOfDay {
			parsed = parsed.Add(24*time.Hour - time.Nanosecond)
		}
	}
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func isOwner(user domain.User) bool {
	return user.Role != nil && *user.Role == "owner"
}
