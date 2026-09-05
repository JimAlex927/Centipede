package http

import (
	"context"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"github.com/gin-gonic/gin"
)

func (handler *Handler) licenseInfo(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	key, err := handler.repository.LicenseKey(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "License not found")
		return
	}
	license, err := handler.licenseService.Verify(key, current.Workspace.ID)
	if key == "" || err != nil {
		writeError(c, http.StatusNotFound, "License not found")
		return
	}
	writeData(c, http.StatusOK, license)
}

func (handler *Handler) activateLicense(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request struct {
		LicenseKey string `json:"licenseKey"`
	}
	if !decode(c, &request) {
		return
	}
	request.LicenseKey = strings.TrimSpace(request.LicenseKey)
	if request.LicenseKey == "" {
		writeError(c, http.StatusBadRequest, "License key is required")
		return
	}
	license, err := handler.licenseService.Verify(request.LicenseKey, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid or expired license")
		return
	}
	count, err := handler.repository.CountActiveUsers(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify license seats")
		return
	}
	if count > int64(license.SeatCount) {
		writeError(c, http.StatusBadRequest, "License seat count is lower than the active member count")
		return
	}
	if err := handler.repository.SetLicenseKey(c.Request.Context(), current.Workspace.ID, request.LicenseKey); err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	actorID := current.User.ID
	resourceID := license.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "license.activated",
		ResourceType: "license", ResourceID: &resourceID,
	})
	writeData(c, http.StatusOK, license)
}

func (handler *Handler) removeLicense(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	if err := handler.repository.RemoveLicenseKey(c.Request.Context(), current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	actorID := current.User.ID
	_ = handler.repository.RecordAudit(c.Request.Context(), postgres.AuditInput{
		WorkspaceID: current.Workspace.ID, ActorID: &actorID, Event: "license.removed",
		ResourceType: "license",
	})
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) entitlementInfo(c *gin.Context) gin.H {
	current := currentPrincipal(c)
	result := gin.H{"cloud": false, "tier": "free", "features": []string{}}
	if handler.licenseService == nil {
		return result
	}
	key, err := handler.repository.LicenseKey(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		return result
	}
	license, err := handler.licenseService.Verify(key, current.Workspace.ID)
	if err != nil || key == "" {
		return result
	}
	result["tier"] = license.LicenseType
	result["features"] = license.Features
	return result
}

func (handler *Handler) requireFeature(c *gin.Context, feature string) bool {
	entitlements := handler.entitlementInfo(c)
	features, _ := entitlements["features"].([]string)
	for _, candidate := range features {
		if candidate == feature {
			return true
		}
	}
	writeError(c, http.StatusForbidden, "This feature requires an active license")
	return false
}

func (handler *Handler) workspaceHasFeature(ctx context.Context, workspaceID, feature string) bool {
	if handler.licenseService == nil {
		return false
	}
	key, err := handler.repository.LicenseKey(ctx, workspaceID)
	if err != nil || key == "" {
		return false
	}
	license, err := handler.licenseService.Verify(key, workspaceID)
	if err != nil {
		return false
	}
	for _, candidate := range license.Features {
		if candidate == feature {
			return true
		}
	}
	return false
}
