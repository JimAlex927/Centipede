package http

import (
	"errors"
	"net/http"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

type pagePermissionRequest struct {
	PageID   string   `json:"pageId"`
	Role     string   `json:"role"`
	UserID   string   `json:"userId"`
	GroupID  string   `json:"groupId"`
	UserIDs  []string `json:"userIds"`
	GroupIDs []string `json:"groupIds"`
	Cursor   string   `json:"cursor"`
	Limit    int      `json:"limit"`
}

func (handler *Handler) registerPagePermissionRoutes(router gin.IRouter) {
	router.POST("/pages/restrict", handler.restrictPage)
	router.POST("/pages/remove-restriction", handler.removePageRestriction)
	router.POST("/pages/add-permission", handler.addPagePermission)
	router.POST("/pages/remove-permission", handler.removePagePermission)
	router.POST("/pages/update-permission", handler.updatePagePermission)
	router.POST("/pages/permissions", handler.pagePermissionMembers)
	router.POST("/pages/permission-info", handler.pagePermissionInfo)
}

func (handler *Handler) pageForPermission(c *gin.Context, pageID string) (domain.Page, principal, bool) {
	current := currentPrincipal(c)
	if pageID == "" {
		writeError(c, http.StatusBadRequest, "pageId is required")
		return domain.Page{}, current, false
	}
	page, err := handler.repository.PageByID(c.Request.Context(), pageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return domain.Page{}, current, false
	}
	return page, current, true
}

func (handler *Handler) canManagePagePermission(c *gin.Context, page domain.Page) bool {
	current := currentPrincipal(c)
	if isAdmin(current.User) {
		return true
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return false
	}
	access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify page permission")
		return false
	}
	if access.HasRestriction {
		if !access.CanAccess || !access.CanEdit {
			writeError(c, http.StatusForbidden, "Forbidden")
			return false
		}
		return true
	}
	return handler.requireSpaceRole(c, page.SpaceID, "writer")
}

func (handler *Handler) restrictPage(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	if err := handler.repository.RestrictPage(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID); err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	handler.publishPagePermissionEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) removePageRestriction(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	if err := handler.repository.RemovePageRestriction(c.Request.Context(), page.ID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	handler.publishPagePermissionEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) addPagePermission(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	if len(request.UserIDs) == 0 && len(request.GroupIDs) == 0 {
		writeError(c, http.StatusBadRequest, "userIds or groupIds is required")
		return
	}
	if err := handler.repository.AddPagePermissions(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, request.Role, request.UserIDs, request.GroupIDs); err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) || err.Error() == "role must be reader or writer" {
			writeError(c, http.StatusBadRequest, err.Error())
		} else {
			handler.writeRepositoryError(c, err, "Member not found")
		}
		return
	}
	handler.publishPagePermissionEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) removePagePermission(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	if len(request.UserIDs) == 0 && len(request.GroupIDs) == 0 {
		writeError(c, http.StatusBadRequest, "userIds or groupIds is required")
		return
	}
	if err := handler.repository.RemovePagePermissions(c.Request.Context(), page.ID, current.Workspace.ID, request.UserIDs, request.GroupIDs); err != nil {
		handler.writeRepositoryError(c, err, "Page restriction not found")
		return
	}
	handler.publishPagePermissionEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) updatePagePermission(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	if (request.UserID == "") == (request.GroupID == "") {
		writeError(c, http.StatusBadRequest, "exactly one of userId or groupId is required")
		return
	}
	var userID, groupID *string
	if request.UserID != "" {
		userID = &request.UserID
	} else {
		groupID = &request.GroupID
	}
	if err := handler.repository.UpdatePagePermissionRole(c.Request.Context(), page.ID, current.Workspace.ID, request.Role, userID, groupID); err != nil {
		if err.Error() == "role must be reader or writer" {
			writeError(c, http.StatusBadRequest, err.Error())
		} else {
			handler.writeRepositoryError(c, err, "Page permission not found")
		}
		return
	}
	handler.publishPagePermissionEvent(current.Workspace.ID, page)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) pagePermissionMembers(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok || !handler.canManagePagePermission(c, page) {
		return
	}
	items, err := handler.repository.PagePermissionMembers(c.Request.Context(), page.ID, current.Workspace.ID, request.Cursor, request.Limit)
	if err != nil {
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid pagination cursor")
			return
		}
		handler.writeRepositoryError(c, err, "Page restriction not found")
		return
	}
	writeData(c, http.StatusOK, items)
}

func (handler *Handler) pagePermissionInfo(c *gin.Context) {
	var request pagePermissionRequest
	if !decode(c, &request) {
		return
	}
	page, current, ok := handler.pageForPermission(c, request.PageID)
	if !ok {
		return
	}
	restriction, err := handler.repository.PageRestriction(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load page restriction")
		return
	}
	access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify page permission")
		return
	}
	canView, canEdit := false, false
	if isAdmin(current.User) {
		canView, canEdit = true, true
	} else if role, roleErr := handler.repository.SpaceRole(c.Request.Context(), page.SpaceID, current.Workspace.ID, current.User.ID); roleErr == nil {
		canView = access.CanAccess
		canEdit = access.CanAccess && access.CanEdit
		if !access.HasRestriction {
			canEdit = role == "writer" || role == "admin"
		}
	}
	writeData(c, http.StatusOK, gin.H{
		"restrictionId":           nonEmptyPointer(restriction.RestrictionID),
		"hasDirectRestriction":    restriction.Direct,
		"hasInheritedRestriction": restriction.Inherited,
		"inheritedFrom":           restriction.InheritedFrom,
		"userAccess": gin.H{
			"canView":   canView,
			"canEdit":   canEdit,
			"canManage": canEdit,
		},
	})
}

func (handler *Handler) publishPagePermissionEvent(workspaceID string, page domain.Page) {
	if handler.realtime != nil {
		handler.realtime.PublishSpaceEvent(workspaceID, page.SpaceID, gin.H{"operation": "refetchRootTreeNodeEvent", "spaceId": page.SpaceID, "pageId": page.ID})
	}
}
