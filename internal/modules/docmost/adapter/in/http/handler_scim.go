package http

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

const maxSCIMTokens = 5

type scimTokenRequest struct {
	TokenID string `json:"tokenId"`
	Name    string `json:"name"`
	Cursor  string `json:"cursor"`
	Limit   int    `json:"limit"`
}

type scimTokenResponse struct {
	postgres.SCIMToken
	Token string `json:"token,omitempty"`
}

func (handler *Handler) scimTokens(c *gin.Context) {
	if !handler.requireFeature(c, "scim") {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request scimTokenRequest
	if !decodeOptional(c, &request) {
		return
	}
	result, err := handler.repository.SCIMTokens(c.Request.Context(), current.Workspace.ID, request.Cursor, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load SCIM tokens")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) createSCIMToken(c *gin.Context) {
	if !handler.requireFeature(c, "scim") {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request scimTokenRequest
	if !decode(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 120 {
		writeError(c, http.StatusBadRequest, "SCIM token name is required and must be at most 120 characters")
		return
	}
	existing, err := handler.repository.SCIMTokens(c.Request.Context(), current.Workspace.ID, "", maxSCIMTokens)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to inspect SCIM tokens")
		return
	}
	if len(existing.Items) >= maxSCIMTokens {
		writeError(c, http.StatusBadRequest, "You can have at most five SCIM tokens")
		return
	}
	plainToken, err := newSCIMToken()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to generate SCIM token")
		return
	}
	digest := sha256.Sum256([]byte(plainToken))
	item, err := handler.repository.CreateSCIMToken(c.Request.Context(), current.Workspace.ID, current.User.ID, name, hex.EncodeToString(digest[:]), plainToken[len(plainToken)-4:])
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create SCIM token")
		return
	}
	actorID, resourceID := current.User.ID, item.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "scim_token.created", ResourceType: "scim_token", ResourceID: &resourceID})
	writeData(c, http.StatusOK, scimTokenResponse{SCIMToken: item, Token: plainToken})
}

func (handler *Handler) updateSCIMToken(c *gin.Context) {
	if !handler.requireFeature(c, "scim") {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request scimTokenRequest
	if !decode(c, &request) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if request.TokenID == "" || name == "" || len(name) > 120 {
		writeError(c, http.StatusBadRequest, "SCIM token id and name are required")
		return
	}
	if err := handler.repository.UpdateSCIMToken(c.Request.Context(), request.TokenID, current.Workspace.ID, name); err != nil {
		handler.writeRepositoryError(c, err, "SCIM token not found")
		return
	}
	actorID, resourceID := current.User.ID, request.TokenID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "scim_token.updated", ResourceType: "scim_token", ResourceID: &resourceID})
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) revokeSCIMToken(c *gin.Context) {
	if !handler.requireFeature(c, "scim") {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request scimTokenRequest
	if !decode(c, &request) || request.TokenID == "" {
		return
	}
	if err := handler.repository.RevokeSCIMToken(c.Request.Context(), request.TokenID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "SCIM token not found")
		return
	}
	actorID, resourceID := current.User.ID, request.TokenID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "scim_token.deleted", ResourceType: "scim_token", ResourceID: &resourceID})
	writeData(c, http.StatusOK, nil)
}

func newSCIMToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "scim_" + base64.RawURLEncoding.EncodeToString(raw), nil
}
