package http

import (
	"errors"
	"net/http"

	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type shareRequest struct {
	ShareID         string `json:"shareId"`
	PageID          string `json:"pageId"`
	IncludeSubPages *bool  `json:"includeSubPages"`
	SearchIndexing  *bool  `json:"searchIndexing"`
	Limit           int    `json:"limit"`
}

func (handler *Handler) shares(c *gin.Context) {
	var request shareRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Shares(c.Request.Context(), current.Workspace.ID, current.User.ID, isAdmin(current.User), request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load shares")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) createShare(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, request.PageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "writer") {
		return
	}
	restricted, err := handler.repository.PageHasRestrictedAncestor(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil || restricted {
		writeError(c, http.StatusForbidden, "Cannot share a restricted page")
		return
	}
	allowed, err := handler.repository.SharingAllowed(c.Request.Context(), current.Workspace.ID, page.SpaceID)
	if err != nil || !allowed {
		writeError(c, http.StatusForbidden, "Public sharing is disabled")
		return
	}
	includeSubPages := request.IncludeSubPages != nil && *request.IncludeSubPages
	searchIndexing := request.SearchIndexing != nil && *request.SearchIndexing
	share, err := handler.repository.CreateShare(c.Request.Context(), page.ID, page.SpaceID, current.Workspace.ID, current.User.ID, includeSubPages, searchIndexing)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to share page")
		return
	}
	writeData(c, http.StatusOK, share)
}

func (handler *Handler) updateShare(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	share, err := handler.repository.ShareByID(c.Request.Context(), request.ShareID)
	if err != nil || share.WorkspaceID != current.Workspace.ID {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	if !handler.requireSpaceRole(c, share.SpaceID, "writer") {
		return
	}
	share, err = handler.repository.UpdateShare(c.Request.Context(), share.ID, current.Workspace.ID, request.IncludeSubPages, request.SearchIndexing)
	if err != nil {
		handler.writeRepositoryError(c, err, "Share not found")
		return
	}
	writeData(c, http.StatusOK, share)
}

func (handler *Handler) deleteShare(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	share, err := handler.repository.ShareByID(c.Request.Context(), request.ShareID)
	if err != nil || share.WorkspaceID != current.Workspace.ID {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	if !handler.requireSpaceRole(c, share.SpaceID, "writer") {
		return
	}
	if err = handler.repository.DeleteShare(c.Request.Context(), share.ID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Share not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) shareForPage(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, request.PageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	share, err := handler.repository.ShareForPage(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Share not found")
		return
	}
	writeData(c, http.StatusOK, share)
}

func (handler *Handler) shareInfo(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	share, err := handler.publicShare(c, request.ShareID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	page, err := handler.repository.PageByID(c.Request.Context(), share.PageID, "", share.WorkspaceID, false)
	if err != nil {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	shared := pageSummary(page)
	share.SharedPage = &shared
	writeData(c, http.StatusOK, share)
}

func (handler *Handler) sharedPageInfo(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	// Requiring the public share key avoids selecting a tenant from an
	// untrusted page id when the frontend and API run on different hosts.
	if request.ShareID == "" || request.PageID == "" {
		writeError(c, http.StatusBadRequest, "shareId and pageId are required")
		return
	}
	share, err := handler.publicShare(c, request.ShareID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Shared page not found")
		return
	}
	contains, err := handler.repository.ShareContainsPage(c.Request.Context(), share, request.PageID)
	if err != nil || !contains {
		writeError(c, http.StatusNotFound, "Shared page not found")
		return
	}
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, request.PageID, share.WorkspaceID, false)
	if err != nil {
		writeError(c, http.StatusNotFound, "Shared page not found")
		return
	}
	restricted, err := handler.repository.PageHasRestrictedAncestor(c.Request.Context(), page.ID, share.WorkspaceID)
	if err != nil || restricted {
		writeError(c, http.StatusNotFound, "Shared page not found")
		return
	}
	writeData(c, http.StatusOK, gin.H{"page": page, "share": share, "features": []string{}})
}

func (handler *Handler) shareTree(c *gin.Context) {
	var request shareRequest
	if !decode(c, &request) {
		return
	}
	share, err := handler.publicShare(c, request.ShareID)
	if err != nil {
		writeError(c, http.StatusNotFound, "Share not found")
		return
	}
	pageTree, err := handler.repository.SharedPageTree(c.Request.Context(), share)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load shared page tree")
		return
	}
	writeData(c, http.StatusOK, gin.H{"share": share, "pageTree": pageTree, "features": []string{}})
}

func (handler *Handler) publicShare(c *gin.Context, shareID string) (domain.Share, error) {
	share, err := handler.repository.ShareByID(c.Request.Context(), shareID)
	if err != nil {
		return domain.Share{}, err
	}
	if _, err = handler.repository.PageByID(c.Request.Context(), share.PageID, "", share.WorkspaceID, false); err != nil {
		return domain.Share{}, errPublicShareDenied
	}
	restricted, err := handler.repository.PageHasRestrictedAncestor(c.Request.Context(), share.PageID, share.WorkspaceID)
	if err != nil || restricted {
		return domain.Share{}, errPublicShareDenied
	}
	allowed, err := handler.repository.SharingAllowed(c.Request.Context(), share.WorkspaceID, share.SpaceID)
	if err != nil || !allowed {
		return domain.Share{}, errPublicShareDenied
	}
	return share, nil
}

func pageSummary(page domain.Page) domain.PageSummary {
	return domain.PageSummary{ID: page.ID, SlugID: page.SlugID, Title: page.Title, Icon: page.Icon, IsBase: page.IsBase, SpaceID: page.SpaceID}
}

var errPublicShareDenied = errors.New("public share is disabled")
