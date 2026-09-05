package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

func (handler *Handler) registerExtraRoutes(router gin.IRouter) {
	router.POST("/groups", handler.groups)
	router.POST("/groups/info", handler.groupInfo)
	router.POST("/groups/create", handler.createGroup)
	router.POST("/groups/update", handler.updateGroup)
	router.POST("/groups/delete", handler.deleteGroup)
	router.POST("/groups/members", handler.groupMembers)
	router.POST("/groups/members/add", handler.addGroupMembers)
	router.POST("/groups/members/remove", handler.removeGroupMember)

	router.POST("/comments", handler.comments)
	router.POST("/comments/info", handler.commentInfo)
	router.POST("/comments/create", handler.createComment)
	router.POST("/comments/update", handler.updateComment)
	router.POST("/comments/resolve", handler.resolveComment)
	router.POST("/comments/delete", handler.deleteComment)

	router.POST("/labels", handler.labels)
	router.POST("/pages/labels", handler.pageLabels)
	router.POST("/pages/labels/add", handler.addPageLabels)
	router.POST("/pages/labels/remove", handler.removePageLabel)

	router.POST("/favorites", handler.favorites)
	router.POST("/favorites/add", handler.addFavorite)
	router.POST("/favorites/remove", handler.removeFavorite)
	router.POST("/favorites/ids", handler.favoriteIDs)

	router.POST("/pages/watch", handler.watchPage)
	router.POST("/pages/unwatch", handler.unwatchPage)
	router.POST("/pages/watch-status", handler.pageWatchStatus)
	router.POST("/spaces/watch", handler.watchSpace)
	router.POST("/spaces/unwatch", handler.unwatchSpace)
	router.POST("/spaces/watch-status", handler.spaceWatchStatus)
	router.POST("/spaces/watched-ids", handler.watchedSpaceIDs)

	router.POST("/sessions", handler.sessions)
	router.POST("/sessions/revoke", handler.revokeSession)
	router.POST("/sessions/revoke-all", handler.revokeAllSessions)

	router.POST("/search", handler.search)
	router.POST("/search/suggest", handler.suggest)
}

type workspaceMemberRequest struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

func (handler *Handler) deactivateWorkspaceMember(c *gin.Context) {
	handler.setWorkspaceMemberState(c, false)
}

func (handler *Handler) activateWorkspaceMember(c *gin.Context) {
	handler.setWorkspaceMemberState(c, true)
}

func (handler *Handler) setWorkspaceMemberState(c *gin.Context, active bool) {
	var request workspaceMemberRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.canManageWorkspaceMember(c, current, request.UserID, false) {
		return
	}
	if !active && request.UserID == current.User.ID {
		writeError(c, http.StatusBadRequest, "You cannot deactivate your own account")
		return
	}
	if err := handler.repository.SetWorkspaceMemberActive(c.Request.Context(), current.Workspace.ID, request.UserID, active); err != nil {
		handler.writeRepositoryError(c, err, "User not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) deleteWorkspaceMember(c *gin.Context) {
	var request workspaceMemberRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.UserID == current.User.ID {
		writeError(c, http.StatusBadRequest, "You cannot delete your own account")
		return
	}
	if !handler.canManageWorkspaceMember(c, current, request.UserID, false) {
		return
	}
	if err := handler.repository.DeleteWorkspaceMember(c.Request.Context(), current.Workspace.ID, request.UserID); err != nil {
		handler.writeRepositoryError(c, err, "User not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) changeWorkspaceMemberRole(c *gin.Context) {
	var request workspaceMemberRequest
	if !decode(c, &request) {
		return
	}
	if request.Role != "owner" && request.Role != "admin" && request.Role != "member" {
		writeError(c, http.StatusBadRequest, "Invalid workspace role")
		return
	}
	current := currentPrincipal(c)
	if !handler.canManageWorkspaceMember(c, current, request.UserID, request.Role == "owner") {
		return
	}
	if request.UserID == current.User.ID && current.User.Role != nil && *current.User.Role == "owner" && request.Role != "owner" {
		owners, err := handler.repository.OwnerCount(c.Request.Context(), current.Workspace.ID)
		if err != nil || owners <= 1 {
			writeError(c, http.StatusBadRequest, "The workspace must keep at least one owner")
			return
		}
	}
	if err := handler.repository.ChangeWorkspaceMemberRole(c.Request.Context(), current.Workspace.ID, request.UserID, request.Role); err != nil {
		handler.writeRepositoryError(c, err, "User not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) canManageWorkspaceMember(c *gin.Context, current principal, targetUserID string, assigningOwner bool) bool {
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return false
	}
	target, err := handler.repository.UserByID(c.Request.Context(), targetUserID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "User not found")
		return false
	}
	currentOwner := current.User.Role != nil && *current.User.Role == "owner"
	targetOwner := target.Role != nil && *target.Role == "owner"
	if (targetOwner || assigningOwner) && !currentOwner {
		writeError(c, http.StatusForbidden, "Only an owner can manage owners")
		return false
	}
	return true
}

type spaceMemberRequest struct {
	SpaceID  string   `json:"spaceId"`
	UserID   *string  `json:"userId"`
	GroupID  *string  `json:"groupId"`
	UserIDs  []string `json:"userIds"`
	GroupIDs []string `json:"groupIds"`
	Role     string   `json:"role"`
	Limit    int      `json:"limit"`
}

func (handler *Handler) spaceMembers(c *gin.Context) {
	var request spaceMemberRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.SpaceMembers(c.Request.Context(), request.SpaceID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load space members")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) addSpaceMembers(c *gin.Context) {
	var request spaceMemberRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	if err := handler.repository.AddSpaceMembers(c.Request.Context(), request.SpaceID, current.Workspace.ID, current.User.ID, request.UserIDs, request.GroupIDs); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to add space members")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) removeSpaceMember(c *gin.Context) {
	var request spaceMemberRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	if request.UserID != nil && *request.UserID == current.User.ID {
		writeError(c, http.StatusBadRequest, "A space admin cannot remove their own membership")
		return
	}
	if err := handler.repository.RemoveSpaceMember(c.Request.Context(), request.SpaceID, current.Workspace.ID, request.UserID, request.GroupID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to remove space member")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) changeSpaceMemberRole(c *gin.Context) {
	var request spaceMemberRequest
	if !decode(c, &request) {
		return
	}
	if request.Role != "admin" && request.Role != "writer" && request.Role != "reader" {
		writeError(c, http.StatusBadRequest, "Invalid space role")
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	if request.UserID != nil && *request.UserID == current.User.ID && request.Role != "admin" {
		writeError(c, http.StatusBadRequest, "A space admin cannot demote their own membership")
		return
	}
	if err := handler.repository.ChangeSpaceMemberRole(c.Request.Context(), request.SpaceID, current.Workspace.ID, request.Role, request.UserID, request.GroupID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to change space member role")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type groupRequest struct {
	GroupID     string   `json:"groupId"`
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	UserIDs     []string `json:"userIds"`
	UserID      string   `json:"userId"`
	Limit       int      `json:"limit"`
}

func (handler *Handler) groups(c *gin.Context) {
	var request groupRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Groups(c.Request.Context(), current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load groups")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) groupInfo(c *gin.Context) {
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	group, err := handler.repository.GroupByID(c.Request.Context(), request.GroupID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Group not found")
		return
	}
	writeData(c, http.StatusOK, group)
}

func (handler *Handler) createGroup(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request groupRequest
	if !decode(c, &request) || request.Name == nil || len(strings.TrimSpace(*request.Name)) < 2 {
		return
	}
	group, err := handler.repository.CreateGroup(c.Request.Context(), current.Workspace.ID, current.User.ID, *request.Name, request.Description, request.UserIDs)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create group")
		return
	}
	writeData(c, http.StatusOK, group)
}

func (handler *Handler) updateGroup(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	group, err := handler.repository.UpdateGroup(c.Request.Context(), request.GroupID, current.Workspace.ID, request.Name, request.Description)
	if err != nil {
		handler.writeRepositoryError(c, err, "Group not found")
		return
	}
	writeData(c, http.StatusOK, group)
}

func (handler *Handler) deleteGroup(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	if err := handler.repository.DeleteGroup(c.Request.Context(), request.GroupID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Group not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) groupMembers(c *gin.Context) {
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.GroupMembers(c.Request.Context(), request.GroupID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load group members")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) addGroupMembers(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	if err := handler.repository.AddGroupUsers(c.Request.Context(), request.GroupID, current.Workspace.ID, request.UserIDs); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to add group members")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) removeGroupMember(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request groupRequest
	if !decode(c, &request) {
		return
	}
	if err := handler.repository.RemoveGroupUser(c.Request.Context(), request.GroupID, current.Workspace.ID, request.UserID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to remove group member")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type commentRequest struct {
	CommentID       string          `json:"commentId"`
	PageID          string          `json:"pageId"`
	Content         json.RawMessage `json:"content"`
	Selection       *string         `json:"selection"`
	Type            *string         `json:"type"`
	ParentCommentID *string         `json:"parentCommentId"`
	Resolved        bool            `json:"resolved"`
	Limit           int             `json:"limit"`
}

func (handler *Handler) comments(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.Comments(c.Request.Context(), request.PageID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load comments")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) commentInfo(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	comment, err := handler.repository.CommentByID(c.Request.Context(), request.CommentID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Comment not found")
		return
	}
	page, err := handler.repository.PageByID(c.Request.Context(), comment.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	writeData(c, http.StatusOK, comment)
}

func (handler *Handler) createComment(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, page.SpaceID, "writer") {
		return
	}
	comment, err := handler.repository.CreateComment(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.CommentInput{PageID: page.ID, Content: normalizeJSON(request.Content), Selection: request.Selection, Type: request.Type, ParentCommentID: request.ParentCommentID, SpaceID: page.SpaceID})
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create comment")
		return
	}
	writeData(c, http.StatusOK, comment)
}

func (handler *Handler) updateComment(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	comment, err := handler.repository.UpdateComment(c.Request.Context(), request.CommentID, current.Workspace.ID, current.User.ID, normalizeJSON(request.Content))
	if err != nil {
		handler.writeRepositoryError(c, err, "Comment not found")
		return
	}
	writeData(c, http.StatusOK, comment)
}

func (handler *Handler) resolveComment(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	comment, err := handler.repository.CommentByID(c.Request.Context(), request.CommentID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Comment not found")
		return
	}
	page, err := handler.repository.PageByID(c.Request.Context(), comment.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "writer") {
		return
	}
	comment, err = handler.repository.ResolveComment(c.Request.Context(), request.CommentID, current.Workspace.ID, current.User.ID, request.Resolved)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to resolve comment")
		return
	}
	writeData(c, http.StatusOK, comment)
}

func (handler *Handler) deleteComment(c *gin.Context) {
	var request commentRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if err := handler.repository.DeleteComment(c.Request.Context(), request.CommentID, current.Workspace.ID, current.User.ID, isAdmin(current.User)); err != nil {
		handler.writeRepositoryError(c, err, "Comment not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type labelRequest struct {
	PageID  string   `json:"pageId"`
	LabelID string   `json:"labelId"`
	Names   []string `json:"names"`
	Limit   int      `json:"limit"`
}

func (handler *Handler) labels(c *gin.Context) {
	var request labelRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Labels(c.Request.Context(), current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load labels")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) pageLabels(c *gin.Context) {
	var request labelRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	result, err := handler.repository.PageLabels(c.Request.Context(), request.PageID, current.Workspace.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load page labels")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) addPageLabels(c *gin.Context) {
	var request labelRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "writer") {
		return
	}
	labels, err := handler.repository.AddPageLabels(c.Request.Context(), request.PageID, current.Workspace.ID, request.Names)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to add labels")
		return
	}
	writeData(c, http.StatusOK, labels)
}

func (handler *Handler) removePageLabel(c *gin.Context) {
	var request labelRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "writer") {
		return
	}
	if err := handler.repository.RemovePageLabel(c.Request.Context(), request.PageID, request.LabelID, current.Workspace.ID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to remove label")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type favoriteRequest struct {
	Type       string  `json:"type"`
	PageID     *string `json:"pageId"`
	SpaceID    *string `json:"spaceId"`
	TemplateID *string `json:"templateId"`
	Limit      int     `json:"limit"`
}

func (request favoriteRequest) input() postgres.FavoriteInput {
	return postgres.FavoriteInput{Type: request.Type, PageID: request.PageID, SpaceID: request.SpaceID, TemplateID: request.TemplateID}
}

func (handler *Handler) favorites(c *gin.Context) {
	var request favoriteRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Favorites(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load favorites")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) addFavorite(c *gin.Context) {
	var request favoriteRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	input := request.input()
	spaceID, err := handler.repository.FavoriteTargetSpace(c.Request.Context(), current.Workspace.ID, input)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) || errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid favorite target")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to verify favorite target")
		}
		return
	}
	if !handler.requireSpaceRole(c, spaceID, "reader") {
		return
	}
	if err := handler.repository.AddFavorite(c.Request.Context(), current.Workspace.ID, current.User.ID, input); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to add favorite")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) removeFavorite(c *gin.Context) {
	var request favoriteRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if err := handler.repository.RemoveFavorite(c.Request.Context(), current.Workspace.ID, current.User.ID, request.input()); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to remove favorite")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) favoriteIDs(c *gin.Context) {
	var request favoriteRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	items, err := handler.repository.FavoriteIDs(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Type, request.SpaceID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load favorite ids")
		return
	}
	writeData(c, http.StatusOK, gin.H{"items": items, "meta": gin.H{"limit": len(items), "hasNextPage": false, "hasPrevPage": false, "nextCursor": nil, "prevCursor": nil}})
}

type watcherRequest struct {
	PageID  string `json:"pageId"`
	SpaceID string `json:"spaceId"`
	Limit   int    `json:"limit"`
}

func (handler *Handler) setPageWatch(c *gin.Context, watching bool) {
	var request watcherRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	if err := handler.repository.SetWatcher(c.Request.Context(), current.Workspace.ID, current.User.ID, page.SpaceID, &page.ID, watching); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to update watcher")
		return
	}
	writeData(c, http.StatusOK, gin.H{"watching": watching})
}

func (handler *Handler) watchPage(c *gin.Context)   { handler.setPageWatch(c, true) }
func (handler *Handler) unwatchPage(c *gin.Context) { handler.setPageWatch(c, false) }

func (handler *Handler) pageWatchStatus(c *gin.Context) {
	var request watcherRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil || !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return
	}
	watching, err := handler.repository.WatchStatus(c.Request.Context(), current.Workspace.ID, current.User.ID, page.SpaceID, &page.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load watcher")
		return
	}
	writeData(c, http.StatusOK, gin.H{"watching": watching})
}

func (handler *Handler) setSpaceWatch(c *gin.Context, watching bool) {
	var request watcherRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	if err := handler.repository.SetWatcher(c.Request.Context(), current.Workspace.ID, current.User.ID, request.SpaceID, nil, watching); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to update watcher")
		return
	}
	writeData(c, http.StatusOK, gin.H{"watching": watching})
}

func (handler *Handler) watchSpace(c *gin.Context)   { handler.setSpaceWatch(c, true) }
func (handler *Handler) unwatchSpace(c *gin.Context) { handler.setSpaceWatch(c, false) }

func (handler *Handler) spaceWatchStatus(c *gin.Context) {
	var request watcherRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "reader") {
		return
	}
	watching, err := handler.repository.WatchStatus(c.Request.Context(), current.Workspace.ID, current.User.ID, request.SpaceID, nil)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load watcher")
		return
	}
	writeData(c, http.StatusOK, gin.H{"watching": watching})
}

func (handler *Handler) watchedSpaceIDs(c *gin.Context) {
	var request watcherRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.WatchedSpaceIDs(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load watched spaces")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) sessions(c *gin.Context) {
	current := currentPrincipal(c)
	items, err := handler.repository.Sessions(c.Request.Context(), current.Workspace.ID, current.User.ID, current.SessionID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load sessions")
		return
	}
	writeData(c, http.StatusOK, gin.H{"sessions": items})
}

func (handler *Handler) revokeSession(c *gin.Context) {
	var request struct {
		SessionID string `json:"sessionId"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.SessionID == current.SessionID {
		writeError(c, http.StatusBadRequest, "Current session cannot be revoked from this endpoint")
		return
	}
	if err := handler.repository.RevokeSession(c.Request.Context(), request.SessionID, current.User.ID, current.Workspace.ID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to revoke session")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) revokeAllSessions(c *gin.Context) {
	current := currentPrincipal(c)
	if err := handler.repository.RevokeOtherSessions(c.Request.Context(), current.Workspace.ID, current.User.ID, current.SessionID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to revoke sessions")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type searchRequest struct {
	Query         string  `json:"query"`
	SpaceID       *string `json:"spaceId"`
	Limit         int     `json:"limit"`
	IncludeUsers  *bool   `json:"includeUsers"`
	IncludeGroups *bool   `json:"includeGroups"`
	IncludePages  *bool   `json:"includePages"`
}

func (handler *Handler) search(c *gin.Context) {
	var request searchRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	items, err := handler.repository.SearchPages(c.Request.Context(), current.Workspace.ID, request.Query, request.SpaceID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to search pages")
		return
	}
	writeData(c, http.StatusOK, gin.H{"items": items})
}

func (handler *Handler) suggest(c *gin.Context) {
	var request searchRequest
	if !decode(c, &request) {
		return
	}
	includeUsers := request.IncludeUsers == nil || *request.IncludeUsers
	includeGroups := request.IncludeGroups == nil || *request.IncludeGroups
	includePages := request.IncludePages == nil || *request.IncludePages
	current := currentPrincipal(c)
	result, err := handler.repository.Suggestions(c.Request.Context(), current.Workspace.ID, request.Query, includeUsers, includeGroups, includePages, request.SpaceID, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load suggestions")
		return
	}
	writeData(c, http.StatusOK, result)
}

func normalizeJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	if value[0] == '"' {
		var decoded string
		if json.Unmarshal(value, &decoded) == nil && json.Valid([]byte(decoded)) {
			return json.RawMessage(decoded)
		}
	}
	return value
}
