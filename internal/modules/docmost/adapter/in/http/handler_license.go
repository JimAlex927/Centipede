package http

import (
	"net/http"
	"strings"

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
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) entitlementInfo(c *gin.Context) gin.H {
	current := currentPrincipal(c)
	result := gin.H{"cloud": false, "tier": "free", "features": []string{}}
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
