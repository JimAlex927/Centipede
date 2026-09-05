package http

import (
	"centipede/internal/modules/docmost/adapter/out/postgres"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func (handler *Handler) labelInfo(c *gin.Context) {
	var request postgres.LabelPageFilter
	if !decode(c, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		writeError(c, http.StatusBadRequest, "Label name is required")
		return
	}
	current := currentPrincipal(c)
	if request.SpaceID != "" && !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	count, err := handler.repository.LabelUsage(c.Request.Context(), current.Workspace.ID, current.User.ID, isAdmin(current.User), request)
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to load label")
		return
	}
	writeData(c, http.StatusOK, gin.H{"name": request.Name, "usageCount": count})
}

func (handler *Handler) labelPages(c *gin.Context) {
	var request postgres.LabelPageFilter
	if !decode(c, &request) {
		return
	}
	if request.LabelID == "" && strings.TrimSpace(request.Name) == "" {
		writeError(c, http.StatusBadRequest, "Label id or name is required")
		return
	}
	current := currentPrincipal(c)
	if request.SpaceID != "" && !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.PagesByLabel(c.Request.Context(), current.Workspace.ID, current.User.ID, isAdmin(current.User), request)
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to load label pages")
		return
	}
	writeData(c, http.StatusOK, result)
}
