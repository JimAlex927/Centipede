package http

import (
	"net/http"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"github.com/gin-gonic/gin"
)

type apiKeyRequest struct {
	APIKeyID  string `json:"apiKeyId"`
	Name      string `json:"name"`
	ExpiresAt string `json:"expiresAt"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
	AdminView bool   `json:"adminView"`
}

type apiKeyResponse struct {
	postgres.APIKey
	Token string `json:"token,omitempty"`
}

func (handler *Handler) apiKeys(c *gin.Context) {
	if !handler.requireFeature(c, "api:keys") {
		return
	}
	var request apiKeyRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.AdminView && !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	result, err := handler.repository.APIKeys(c.Request.Context(), current.Workspace.ID, current.User.ID, request.AdminView, request.Cursor, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load API keys")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) createAPIKey(c *gin.Context) {
	if !handler.requireFeature(c, "api:keys") {
		return
	}
	var request apiKeyRequest
	if !decode(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 120 {
		writeError(c, http.StatusBadRequest, "API key name is required and must be at most 120 characters")
		return
	}
	expiresAt, err := parseAPIKeyExpiry(request.ExpiresAt)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid API key expiry")
		return
	}
	if expiresAt != nil && !expiresAt.After(time.Now().UTC()) {
		writeError(c, http.StatusBadRequest, "API key expiry must be in the future")
		return
	}
	current := currentPrincipal(c)
	key, err := handler.repository.CreateAPIKey(c.Request.Context(), current.Workspace.ID, current.User.ID, name, expiresAt)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create API key")
		return
	}
	actorID := current.User.ID
	resourceID := key.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "api_key.created",
		ResourceType: "api_key", ResourceID: &resourceID,
	})
	ttl := 10 * 365 * 24 * time.Hour
	if expiresAt != nil {
		ttl = time.Until(*expiresAt)
	}
	token, err := handler.tokens.issue(tokenClaims{Subject: current.User.ID, WorkspaceID: current.Workspace.ID, Type: "api_key", APIKeyID: key.ID}, ttl)
	if err != nil {
		_ = handler.repository.RevokeAPIKey(c.Request.Context(), key.ID, current.Workspace.ID, current.User.ID, true)
		writeError(c, http.StatusInternalServerError, "Failed to issue API key token")
		return
	}
	writeData(c, http.StatusOK, apiKeyResponse{APIKey: key, Token: token})
}

func (handler *Handler) updateAPIKey(c *gin.Context) {
	if !handler.requireFeature(c, "api:keys") {
		return
	}
	var request apiKeyRequest
	if !decode(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if request.APIKeyID == "" || name == "" || len(name) > 120 {
		writeError(c, http.StatusBadRequest, "API key id and name are required")
		return
	}
	current := currentPrincipal(c)
	key, err := handler.repository.UpdateAPIKey(c.Request.Context(), request.APIKeyID, current.Workspace.ID, current.User.ID, name, isAdmin(current.User))
	if err != nil {
		handler.writeRepositoryError(c, err, "API key not found")
		return
	}
	actorID := current.User.ID
	resourceID := key.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "api_key.updated",
		ResourceType: "api_key", ResourceID: &resourceID,
	})
	writeData(c, http.StatusOK, key)
}

func (handler *Handler) revokeAPIKey(c *gin.Context) {
	if !handler.requireFeature(c, "api:keys") {
		return
	}
	var request apiKeyRequest
	if !decode(c, &request) || request.APIKeyID == "" {
		return
	}
	current := currentPrincipal(c)
	if err := handler.repository.RevokeAPIKey(c.Request.Context(), request.APIKeyID, current.Workspace.ID, current.User.ID, isAdmin(current.User)); err != nil {
		handler.writeRepositoryError(c, err, "API key not found")
		return
	}
	actorID := current.User.ID
	resourceID := request.APIKeyID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "api_key.deleted",
		ResourceType: "api_key", ResourceID: &resourceID,
	})
	writeData(c, http.StatusOK, nil)
}

func parseAPIKeyExpiry(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", value)
	}
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
