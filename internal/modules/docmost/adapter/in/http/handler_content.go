package http

import (
	"errors"
	"net/http"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

type pageContentRequest struct {
	PageID    string `json:"pageId"`
	SpaceID   string `json:"spaceId"`
	HistoryID string `json:"historyId"`
	Direction string `json:"direction"`
	Limit     int    `json:"limit"`
}

func (handler *Handler) duplicatePage(c *gin.Context) {
	var request pageContentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	source, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, source.SpaceID, "writer") {
		return
	}
	if request.SpaceID != "" && request.SpaceID != source.SpaceID && !handler.requireSpaceRole(c, request.SpaceID, "writer") {
		return
	}
	duplicated, childIDs, err := handler.repository.DuplicatePage(c.Request.Context(), source.ID, request.SpaceID, current.Workspace.ID, current.User.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Failed to duplicate page")
		return
	}
	result := withPagePermissions(duplicated)
	result["childPageIds"] = childIDs
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) backlinkCount(c *gin.Context) {
	var request pageContentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	incoming, outgoing, err := handler.repository.BacklinkCount(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, isAdmin(current.User))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to count backlinks")
		return
	}
	writeData(c, http.StatusOK, gin.H{"incoming": incoming, "outgoing": outgoing})
}

func (handler *Handler) backlinks(c *gin.Context) {
	var request pageContentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.BacklinkPages(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, request.Direction, isAdmin(current.User), request.Limit)
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Direction must be incoming or outgoing")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to load backlinks")
		}
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) pageHistory(c *gin.Context) {
	var request pageContentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.PageHistory(c.Request.Context(), page.ID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load page history")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) pageHistoryInfo(c *gin.Context) {
	var request pageContentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	history, err := handler.repository.PageHistoryByID(c.Request.Context(), request.HistoryID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page history not found")
		return
	}
	page, err := handler.repository.PageByID(c.Request.Context(), history.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	writeData(c, http.StatusOK, history)
}
