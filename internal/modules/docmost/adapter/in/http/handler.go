package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/application"
	"centipede/internal/modules/docmost/domain"
	"centipede/internal/platform/config"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

const principalKey = "docmost_principal"

type Handler struct {
	repository   *postgres.Repository
	tokens       *tokenService
	cookieTTL    time.Duration
	cookieSecure bool
	frontendURL  string
	publicURL    string
	legacyURL    string
	mailer       application.Mailer
	storage      application.Storage
	maxUpload    int64
	pdfOCR       config.PDFOCRConfig
	realtime     *RealtimeHandler
}

func (handler *Handler) SetRealtimeHandler(realtime *RealtimeHandler) {
	handler.realtime = realtime
}

type principal struct {
	User      domain.User
	Workspace domain.Workspace
	SessionID string
}

func NewHandler(repository *postgres.Repository, secret string, cookieTTL time.Duration, cookieSecure bool, frontendURL, publicURL, legacyURL string, mailer application.Mailer, storage application.Storage, maxUpload int64, pdfOCR config.PDFOCRConfig) *Handler {
	return &Handler{
		repository: repository, tokens: newTokenService(secret),
		cookieTTL: cookieTTL, cookieSecure: cookieSecure,
		frontendURL: strings.TrimRight(frontendURL, "/"), publicURL: strings.TrimRight(publicURL, "/"), legacyURL: strings.TrimRight(legacyURL, "/"),
		mailer: mailer, storage: storage, maxUpload: maxUpload, pdfOCR: pdfOCR,
	}
}

func (handler *Handler) Register(router gin.IRouter) {
	// Public share pages are rendered by the separately deployed React client.
	// Keep the upstream share URL shape working without making the Go API serve
	// a second copy of the frontend bundle.
	router.GET("/share/p/:pageSlug", handler.sharePageRedirect)
	router.GET("/share/:shareID/p/:pageSlug", handler.sharePageRedirect)

	api := router.Group("/api")

	api.POST("/workspace/public", handler.publicWorkspace)
	api.POST("/workspace/check-hostname", handler.checkHostname)
	api.POST("/auth/setup", handler.setup)
	api.POST("/auth/login", handler.login)
	api.POST("/auth/forgot-password", handler.forgotPassword)
	api.POST("/auth/password-reset", handler.passwordReset)
	api.POST("/auth/verify-token", handler.verifyUserToken)
	api.POST("/workspace/invites/info", handler.invitationInfo)
	api.POST("/workspace/invites/accept", handler.acceptInvitation)
	api.POST("/shares/info", handler.shareInfo)
	api.POST("/shares/page-info", handler.sharedPageInfo)
	api.POST("/shares/tree", handler.shareTree)
	api.POST("/search/share-search", handler.shareSearch)
	api.POST("/shares/transclusion/lookup", handler.shareTransclusionLookup)
	api.GET("/files/public/:fileId/:fileName", handler.getPublicFile)
	api.GET("/attachments/img/:attachmentType/:fileName", handler.getPublicImage)
	api.POST("/version", handler.version)

	protected := api.Group("")
	protected.Use(handler.authenticate())
	protected.POST("/auth/logout", handler.logout)
	protected.POST("/auth/collab-token", handler.collabToken)
	protected.POST("/auth/change-password", handler.changePassword)
	protected.POST("/users/me", handler.me)
	protected.POST("/users/update", handler.updateUser)

	protected.POST("/workspace/info", handler.workspaceInfo)
	protected.POST("/workspace/entitlements", handler.entitlements)
	protected.POST("/workspace/update", handler.updateWorkspace)
	protected.POST("/workspace/members", handler.workspaceMembers)
	protected.POST("/workspace/members/deactivate", handler.deactivateWorkspaceMember)
	protected.POST("/workspace/members/activate", handler.activateWorkspaceMember)
	protected.POST("/workspace/members/delete", handler.deleteWorkspaceMember)
	protected.POST("/workspace/members/change-role", handler.changeWorkspaceMemberRole)
	protected.POST("/workspace/invites", handler.invitations)
	protected.POST("/workspace/invites/create", handler.createInvitations)
	protected.POST("/workspace/invites/resend", handler.resendInvitation)
	protected.POST("/workspace/invites/revoke", handler.revokeInvitation)
	protected.POST("/workspace/invites/link", handler.invitationLink)
	protected.POST("/shares", handler.shares)
	protected.POST("/shares/create", handler.createShare)
	protected.POST("/shares/update", handler.updateShare)
	protected.POST("/shares/delete", handler.deleteShare)
	protected.POST("/shares/for-page", handler.shareForPage)
	protected.POST("/notifications", handler.notifications)
	protected.POST("/notifications/unread-count", handler.unreadNotificationCount)
	protected.POST("/notifications/mark-read", handler.markNotificationsRead)
	protected.POST("/notifications/mark-all-read", handler.markAllNotificationsRead)
	protected.POST("/files/upload", handler.uploadFile)
	protected.POST("/files/info", handler.attachmentInfo)
	protected.GET("/files/:fileId/:fileName", handler.getFile)
	protected.POST("/pages/attachments", handler.pageAttachments)
	protected.POST("/attachments/upload-image", handler.uploadImage)
	protected.POST("/attachments/remove-icon", handler.removeIcon)

	protected.POST("/spaces", handler.spaces)
	protected.POST("/spaces/info", handler.spaceInfo)
	protected.POST("/spaces/create", handler.createSpace)
	protected.POST("/spaces/update", handler.updateSpace)
	protected.POST("/spaces/delete", handler.deleteSpace)
	protected.POST("/spaces/members", handler.spaceMembers)
	protected.POST("/spaces/members/add", handler.addSpaceMembers)
	protected.POST("/spaces/members/remove", handler.removeSpaceMember)
	protected.POST("/spaces/members/change-role", handler.changeSpaceMemberRole)

	protected.POST("/pages/info", handler.pageInfo)
	protected.POST("/pages/create", handler.createPage)
	protected.POST("/pages/update", handler.updatePage)
	protected.POST("/pages/delete", handler.deletePage)
	protected.POST("/pages/restore", handler.restorePage)
	protected.POST("/pages/move", handler.movePage)
	protected.POST("/pages/move-to-space", handler.movePageToSpace)
	protected.POST("/pages/duplicate", handler.duplicatePage)
	protected.POST("/pages/sidebar-pages", handler.sidebarPages)
	protected.POST("/pages/recent", handler.recentPages)
	protected.POST("/pages/created-by-user", handler.createdByUser)
	protected.POST("/pages/trash", handler.trashPages)
	protected.POST("/pages/breadcrumbs", handler.breadcrumbs)
	protected.POST("/pages/backlinks-count", handler.backlinkCount)
	protected.POST("/pages/backlinks", handler.backlinks)
	protected.POST("/pages/history", handler.pageHistory)
	protected.POST("/pages/history/info", handler.pageHistoryInfo)
	protected.POST("/pages/export", handler.exportPage)
	protected.POST("/spaces/export", handler.exportSpace)
	protected.POST("/docx-export", handler.exportDocx)
	protected.POST("/pages/import", handler.importPage)
	protected.POST("/pages/import-zip", handler.importZip)
	protected.POST("/pages/transclusion/lookup", handler.transclusionLookup)
	protected.POST("/pages/transclusion/references", handler.transclusionReferences)
	protected.POST("/pages/transclusion/unsync-reference", handler.unsyncTransclusionReference)
	protected.POST("/file-tasks", handler.fileTasks)
	protected.POST("/file-tasks/info", handler.fileTaskInfo)

	handler.registerExtraRoutes(protected)

	protected.GET("/migration/status", handler.migrationStatus)
}

func (handler *Handler) sharePageRedirect(c *gin.Context) {
	if handler.frontendURL == "" {
		c.Status(http.StatusNotFound)
		return
	}
	target := strings.TrimRight(handler.frontendURL, "/") + c.Request.URL.EscapedPath()
	if c.Request.URL.RawQuery != "" {
		target += "?" + c.Request.URL.RawQuery
	}
	c.Redirect(http.StatusTemporaryRedirect, target)
}

func (handler *Handler) authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := ""
		if cookie, err := c.Request.Cookie("authToken"); err == nil {
			raw = cookie.Value
		}
		if raw == "" {
			header := c.GetHeader("Authorization")
			if strings.HasPrefix(header, "Bearer ") {
				raw = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
			}
		}
		claims, err := handler.tokens.parse(raw, "access")
		if err != nil {
			writeError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		user, err := handler.repository.UserByID(c.Request.Context(), claims.Subject, claims.WorkspaceID)
		if err != nil || user.DeactivatedAt != nil || user.DeletedAt != nil {
			writeError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		if claims.SessionID != "" {
			active, sessionErr := handler.repository.SessionActive(c.Request.Context(), claims.SessionID, claims.Subject, claims.WorkspaceID)
			if sessionErr != nil || !active {
				writeError(c, http.StatusUnauthorized, "Unauthorized")
				c.Abort()
				return
			}
		}
		workspace, err := handler.repository.WorkspaceByID(c.Request.Context(), claims.WorkspaceID)
		if err != nil {
			writeError(c, http.StatusUnauthorized, "Unauthorized")
			c.Abort()
			return
		}
		c.Set(principalKey, principal{User: user, Workspace: workspace, SessionID: claims.SessionID})
		c.Next()
	}
}

func (handler *Handler) publicWorkspace(c *gin.Context) {
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	writeData(c, http.StatusOK, gin.H{
		"id": workspace.ID, "name": workspace.Name, "logo": workspace.Logo,
		"hostname": workspace.Hostname, "enforceSso": workspace.EnforceSSO,
		"authProviders": []any{},
	})
}

func (handler *Handler) checkHostname(c *gin.Context) {
	var request struct {
		Hostname string `json:"hostname"`
	}
	if !decode(c, &request) {
		return
	}
	hostname := strings.ToLower(strings.TrimSpace(request.Hostname))
	if hostname == "" {
		writeError(c, http.StatusBadRequest, "Hostname is required")
		return
	}
	exists, err := handler.repository.HostnameExists(c.Request.Context(), hostname)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to check hostname")
		return
	}
	if exists {
		writeError(c, http.StatusBadRequest, "Hostname already exists")
		return
	}
	writeData(c, http.StatusOK, gin.H{"hostname": hostname})
}

func (handler *Handler) setup(c *gin.Context) {
	if _, err := handler.repository.FirstWorkspace(c.Request.Context()); err == nil {
		writeError(c, http.StatusConflict, "Workspace is already configured")
		return
	} else if !errors.Is(err, postgres.ErrNotFound) {
		writeError(c, http.StatusInternalServerError, "Failed to inspect workspace")
		return
	}
	var request struct {
		WorkspaceName string `json:"workspaceName"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
	}
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.Name) == "" || !strings.Contains(request.Email, "@") || len(request.Password) < 8 {
		writeError(c, http.StatusBadRequest, "Name, valid email and password of at least 8 characters are required")
		return
	}
	if strings.TrimSpace(request.WorkspaceName) == "" {
		request.WorkspaceName = "My workspace"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to secure password")
		return
	}
	workspace, user, err := handler.repository.Setup(c.Request.Context(), postgres.SetupInput{
		WorkspaceName: strings.TrimSpace(request.WorkspaceName), Name: strings.TrimSpace(request.Name),
		Email: strings.TrimSpace(request.Email), PasswordHash: string(hash),
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create workspace")
		return
	}
	if err := handler.startSession(c, user); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
	writeData(c, http.StatusOK, workspace)
}

func (handler *Handler) login(c *gin.Context) {
	var request struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(c, &request) {
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	user, err := handler.repository.UserByEmail(c.Request.Context(), request.Email, workspace.ID)
	if err != nil || user.Password == nil || bcrypt.CompareHashAndPassword([]byte(*user.Password), []byte(request.Password)) != nil || user.DeactivatedAt != nil {
		writeError(c, http.StatusUnauthorized, "Email or password does not match")
		return
	}
	if err := handler.repository.MarkLogin(c.Request.Context(), user.ID, workspace.ID); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to update login")
		return
	}
	if err := handler.startSession(c, user); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) startSession(c *gin.Context, user domain.User) error {
	if user.WorkspaceID == nil {
		return errors.New("user has no workspace")
	}
	expiresAt := time.Now().UTC().Add(handler.cookieTTL)
	sessionID, err := handler.repository.CreateSession(c.Request.Context(), user.ID, *user.WorkspaceID, c.GetHeader("User-Agent"), c.ClientIP(), expiresAt)
	if err != nil {
		return err
	}
	token, err := handler.tokens.issue(tokenClaims{
		Subject: user.ID, Email: user.Email, WorkspaceID: *user.WorkspaceID,
		Type: "access", SessionID: sessionID,
	}, handler.cookieTTL)
	if err != nil {
		return err
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: "authToken", Value: token, Path: "/", Expires: expiresAt,
		MaxAge: int(handler.cookieTTL.Seconds()), HttpOnly: true,
		Secure: handler.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (handler *Handler) logout(c *gin.Context) {
	current := currentPrincipal(c)
	if current.SessionID != "" {
		_ = handler.repository.RevokeSession(c.Request.Context(), current.SessionID, current.User.ID, current.Workspace.ID)
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: "authToken", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: handler.cookieSecure, SameSite: http.SameSiteLaxMode})
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) collabToken(c *gin.Context) {
	current := currentPrincipal(c)
	token, err := handler.tokens.issue(tokenClaims{Subject: current.User.ID, WorkspaceID: current.Workspace.ID, Type: "collab"}, 24*time.Hour)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to issue collaboration token")
		return
	}
	writeData(c, http.StatusOK, gin.H{"token": token})
}

func (handler *Handler) me(c *gin.Context) {
	current := currentPrincipal(c)
	count, err := handler.repository.CountActiveUsers(c.Request.Context(), current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load workspace")
		return
	}
	current.Workspace.MemberCount = count
	writeData(c, http.StatusOK, gin.H{"user": current.User, "workspace": current.Workspace})
}

func (handler *Handler) updateUser(c *gin.Context) {
	var request struct {
		Name      *string         `json:"name"`
		Locale    *string         `json:"locale"`
		Timezone  *string         `json:"timezone"`
		AvatarURL *string         `json:"avatarUrl"`
		Settings  json.RawMessage `json:"settings"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	user, err := handler.repository.UpdateUser(c.Request.Context(), current.User.ID, current.Workspace.ID, postgres.UserUpdate{
		Name: request.Name, Locale: request.Locale, Timezone: request.Timezone,
		AvatarURL: request.AvatarURL, Settings: request.Settings,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to update user")
		return
	}
	writeData(c, http.StatusOK, user)
}

func (handler *Handler) workspaceInfo(c *gin.Context) {
	writeData(c, http.StatusOK, currentPrincipal(c).Workspace)
}

func (handler *Handler) entitlements(c *gin.Context) {
	writeData(c, http.StatusOK, gin.H{"cloud": false, "tier": "community", "features": []string{}})
}

func (handler *Handler) updateWorkspace(c *gin.Context) {
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	var request struct {
		Name        *string         `json:"name"`
		Description *string         `json:"description"`
		Logo        *string         `json:"logo"`
		Hostname    *string         `json:"hostname"`
		Settings    json.RawMessage `json:"settings"`
	}
	if !decode(c, &request) {
		return
	}
	workspace, err := handler.repository.UpdateWorkspace(c.Request.Context(), current.Workspace.ID, postgres.WorkspaceUpdate{
		Name: request.Name, Description: request.Description, Logo: request.Logo,
		Hostname: request.Hostname, Settings: request.Settings,
	})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to update workspace")
		return
	}
	writeData(c, http.StatusOK, workspace)
}

func (handler *Handler) workspaceMembers(c *gin.Context) {
	var request paginationRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.WorkspaceMembers(c.Request.Context(), current.Workspace.ID, request.limit())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load workspace members")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) spaces(c *gin.Context) {
	var request paginationRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Spaces(c.Request.Context(), current.Workspace.ID, current.User.ID, request.limit())
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load spaces")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) spaceInfo(c *gin.Context) {
	var request struct {
		SpaceID string `json:"spaceId"`
	}
	if !decode(c, &request) {
		return
	}
	if strings.TrimSpace(request.SpaceID) == "" {
		writeError(c, http.StatusBadRequest, "Space id is required")
		return
	}
	current := currentPrincipal(c)
	space, err := handler.repository.SpaceByID(c.Request.Context(), request.SpaceID, current.Workspace.ID, current.User.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Space not found")
		return
	}
	if space.Membership != nil {
		space.Membership.Permissions = spacePermissions(space.Membership.Role)
	}
	writeData(c, http.StatusOK, space)
}

type spaceRequest struct {
	SpaceID     string          `json:"spaceId"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Slug        *string         `json:"slug"`
	Logo        *string         `json:"logo"`
	Visibility  *string         `json:"visibility"`
	DefaultRole *string         `json:"defaultRole"`
	Settings    json.RawMessage `json:"settings"`
}

func (request spaceRequest) input() postgres.SpaceInput {
	return postgres.SpaceInput{Name: request.Name, Description: request.Description, Slug: request.Slug, Logo: request.Logo, Visibility: request.Visibility, DefaultRole: request.DefaultRole, Settings: request.Settings}
}

func (handler *Handler) createSpace(c *gin.Context) {
	var request spaceRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	space, err := handler.repository.CreateSpace(c.Request.Context(), current.Workspace.ID, current.User.ID, request.input())
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create space")
		return
	}
	writeData(c, http.StatusOK, space)
}

func (handler *Handler) updateSpace(c *gin.Context) {
	var request spaceRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	space, err := handler.repository.UpdateSpace(c.Request.Context(), request.SpaceID, current.Workspace.ID, current.User.ID, request.input())
	if err != nil {
		handler.writeRepositoryError(c, err, "Space not found")
		return
	}
	writeData(c, http.StatusOK, space)
}

func (handler *Handler) deleteSpace(c *gin.Context) {
	var request struct {
		SpaceID string `json:"spaceId"`
	}
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	if err := handler.repository.DeleteSpace(c.Request.Context(), request.SpaceID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Space not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

type pageRequest struct {
	PageID       string          `json:"pageId"`
	SlugID       string          `json:"slugId"`
	Title        *string         `json:"title"`
	Icon         *string         `json:"icon"`
	CoverPhoto   *string         `json:"coverPhoto"`
	Position     *string         `json:"position"`
	ParentPageID *string         `json:"parentPageId"`
	SpaceID      *string         `json:"spaceId"`
	IsLocked     *bool           `json:"isLocked"`
	Content      json.RawMessage `json:"content"`
	Permanently  bool            `json:"permanentlyDelete"`
	Limit        int             `json:"limit"`
	UserID       *string         `json:"userId"`
}

func (request pageRequest) input() postgres.PageInput {
	return postgres.PageInput{Title: request.Title, Icon: request.Icon, CoverPhoto: request.CoverPhoto, Position: request.Position, ParentPageID: request.ParentPageID, SpaceID: request.SpaceID, IsLocked: request.IsLocked, Content: request.Content}
}

func (handler *Handler) pageInfo(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	lookup := request.PageID
	if lookup == "" {
		lookup = request.SlugID
	}
	// Docmost's client sends the URL token as pageId, even though the
	// token is normally pages.slug_id rather than the UUID primary key.
	// Passing it through both lookup slots keeps refresh/deep-link loads
	// compatible with both the UUID and slug-id forms.
	page, err := handler.repository.PageByID(c.Request.Context(), lookup, lookup, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requirePageAccess(c, page, false) {
		return
	}
	result := withPagePermissions(page)
	permissions, permissionsErr := handler.pagePermissions(c.Request.Context(), page, current)
	if permissionsErr != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify page permission")
		return
	}
	result["permissions"] = permissions
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) createPage(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if request.SpaceID == nil || !handler.requireSpaceRole(c, *request.SpaceID, "writer") {
		return
	}
	page, err := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, request.input())
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	writeData(c, http.StatusOK, withPagePermissions(page))
}

func (handler *Handler) updatePage(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, existing.SpaceID, "writer") {
		return
	}
	page, err := handler.repository.UpdatePage(c.Request.Context(), request.PageID, current.Workspace.ID, current.User.ID, request.input())
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	handler.notifyPageUpdated(c.Request.Context(), page, current.User.ID)
	writeData(c, http.StatusOK, withPagePermissions(page))
}

func (handler *Handler) deletePage(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, true)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	requiredRole := "writer"
	if request.Permanently {
		requiredRole = "admin"
	}
	if !handler.requireSpaceRole(c, existing.SpaceID, requiredRole) {
		return
	}
	if existing.DeletedAt == nil && !handler.requirePageAccess(c, existing, true) {
		return
	}
	if err := handler.repository.DeletePage(c.Request.Context(), request.PageID, current.Workspace.ID, current.User.ID, request.Permanently); err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) restorePage(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, true)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requireSpaceRole(c, existing.SpaceID, "writer") {
		return
	}
	page, err := handler.repository.RestorePage(c.Request.Context(), request.PageID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	writeData(c, http.StatusOK, withPagePermissions(page))
}

func (handler *Handler) movePage(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requirePageAccess(c, existing, true) {
		return
	}
	if err := handler.repository.MovePage(c.Request.Context(), request.PageID, current.Workspace.ID, request.ParentPageID, request.Position); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to move page")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) movePageToSpace(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) || request.SpaceID == nil {
		return
	}
	current := currentPrincipal(c)
	existing, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requirePageAccess(c, existing, true) || !handler.requireSpaceRole(c, *request.SpaceID, "writer") {
		return
	}
	if err := handler.repository.MovePageToSpace(c.Request.Context(), request.PageID, current.Workspace.ID, *request.SpaceID); err != nil {
		writeError(c, http.StatusBadRequest, "Failed to move page")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) sidebarPages(c *gin.Context) {
	var request pageRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Pages(c.Request.Context(), current.Workspace.ID, postgres.PageListFilter{ViewerID: current.User.ID, ViewerAdmin: isAdmin(current.User), SpaceID: request.SpaceID, ParentPageID: nonEmptyPointer(request.PageID), Limit: request.Limit})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load pages")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) recentPages(c *gin.Context) {
	var request pageRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Pages(c.Request.Context(), current.Workspace.ID, postgres.PageListFilter{ViewerID: current.User.ID, ViewerAdmin: isAdmin(current.User), SpaceID: request.SpaceID, Recent: true, Limit: request.Limit})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load recent pages")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) createdByUser(c *gin.Context) {
	var request pageRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	creatorID := request.UserID
	if creatorID == nil {
		creatorID = &current.User.ID
	}
	result, err := handler.repository.Pages(c.Request.Context(), current.Workspace.ID, postgres.PageListFilter{ViewerID: current.User.ID, ViewerAdmin: isAdmin(current.User), SpaceID: request.SpaceID, CreatorID: creatorID, Recent: true, Limit: request.Limit})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load pages")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) trashPages(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	result, err := handler.repository.Pages(c.Request.Context(), current.Workspace.ID, postgres.PageListFilter{ViewerID: current.User.ID, ViewerAdmin: isAdmin(current.User), SpaceID: request.SpaceID, Deleted: true, Recent: true, Limit: request.Limit})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load trash")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) breadcrumbs(c *gin.Context) {
	var request pageRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, "", current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requirePageAccess(c, page, false) {
		return
	}
	items, err := handler.repository.Breadcrumbs(c.Request.Context(), page.ID, current.Workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load breadcrumbs")
		return
	}
	writeData(c, http.StatusOK, items)
}

func (handler *Handler) version(c *gin.Context) {
	writeData(c, http.StatusOK, gin.H{"currentVersion": "go-migration", "latestVersion": "go-migration", "releaseUrl": ""})
}

func (handler *Handler) migrationStatus(c *gin.Context) {
	legacyFallbackConfigured := handler.legacyURL != ""
	writeData(c, http.StatusOK, gin.H{
		"runtime": "go", "nodeRequired": legacyFallbackConfigured, "legacyFallbackConfigured": legacyFallbackConfigured,
		"implemented": []string{"auth-core", "users", "workspace-core", "spaces-core", "pages-core", "groups-core", "comments-core", "search-core", "shared-page-search", "attachment-search", "shares-core", "shared-attachments", "local-attachments", "file-task-query", "notifications-core", "sessions", "page-history", "collaboration-core", "realtime-core", "transclusion-lookup", "transclusion-attachment-copy", "mail-delivery", "database-integration-validation", "docmost-schema-adoption", "page-access-core", "page-permissions-management", "single-page-export", "archive-export", "export-attachments", "docx-export", "docx-import", "pdf-text-import", "markdown-html-import", "generic-zip-import", "zip-attachment-import", "notion-confluence-basic-import"},
		"pending":     []string{"pdf-ocr-import", "notion-confluence-full-import", "docmost-schema-upgrades", "enterprise"},
	})
}

type paginationRequest struct {
	Limit int `json:"limit"`
}

func (request paginationRequest) limit() int {
	if request.Limit <= 0 {
		return 50
	}
	if request.Limit > 100 {
		return 100
	}
	return request.Limit
}

func withPagePermissions(page domain.Page) gin.H {
	contents, _ := json.Marshal(page)
	var result gin.H
	_ = json.Unmarshal(contents, &result)
	result["permissions"] = gin.H{"canEdit": true, "hasRestriction": false}
	return result
}

func (handler *Handler) requirePageAccess(c *gin.Context, page domain.Page, edit bool) bool {
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, page.SpaceID, "reader") {
		return false
	}
	access, err := handler.repository.PageAccess(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify page permission")
		return false
	}
	if access.HasRestriction && (!access.CanAccess || (edit && !access.CanEdit)) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return false
	}
	if edit && !access.HasRestriction {
		return handler.requireSpaceRole(c, page.SpaceID, "writer")
	}
	return true
}

func (handler *Handler) pagePermissions(ctx context.Context, page domain.Page, current principal) (gin.H, error) {
	access, err := handler.repository.PageAccess(ctx, page.ID, current.Workspace.ID, current.User.ID)
	if err != nil {
		return nil, err
	}
	canEdit := access.CanEdit
	if !access.HasRestriction {
		if isAdmin(current.User) {
			return gin.H{"canEdit": true, "hasRestriction": false}, nil
		}
		role, roleErr := handler.repository.SpaceRole(ctx, page.SpaceID, current.Workspace.ID, current.User.ID)
		if roleErr != nil {
			return nil, roleErr
		}
		canEdit = isAdmin(current.User) || role == "writer" || role == "admin"
	}
	return gin.H{"canEdit": canEdit, "hasRestriction": access.HasRestriction}, nil
}

func currentPrincipal(c *gin.Context) principal {
	value, _ := c.Get(principalKey)
	current, _ := value.(principal)
	return current
}

func isAdmin(user domain.User) bool {
	return user.Role != nil && (*user.Role == "owner" || *user.Role == "admin")
}

func spacePermissions(role string) []domain.Permission {
	switch role {
	case "admin":
		return []domain.Permission{
			{Action: "manage", Subject: "settings"},
			{Action: "manage", Subject: "member"},
			{Action: "manage", Subject: "page"},
			{Action: "manage", Subject: "share"},
		}
	case "writer":
		return []domain.Permission{
			{Action: "read", Subject: "settings"},
			{Action: "read", Subject: "member"},
			{Action: "manage", Subject: "page"},
			{Action: "manage", Subject: "share"},
		}
	case "reader":
		return []domain.Permission{
			{Action: "read", Subject: "settings"},
			{Action: "read", Subject: "member"},
			{Action: "read", Subject: "page"},
			{Action: "read", Subject: "share"},
		}
	default:
		return []domain.Permission{}
	}
}

func (handler *Handler) requireSpaceRole(c *gin.Context, spaceID, minimum string) bool {
	current := currentPrincipal(c)
	if spaceID == "" {
		writeError(c, http.StatusBadRequest, "Space id is required")
		return false
	}
	if _, err := handler.repository.SpaceByID(c.Request.Context(), spaceID, current.Workspace.ID, current.User.ID); err != nil {
		handler.writeRepositoryError(c, err, "Space not found")
		return false
	}
	if isAdmin(current.User) {
		return true
	}
	role, err := handler.repository.SpaceRole(c.Request.Context(), spaceID, current.Workspace.ID, current.User.ID)
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusForbidden, "Forbidden")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to verify space permission")
		}
		return false
	}
	weight := map[string]int{"reader": 1, "writer": 2, "admin": 3}
	if weight[minimum] == 0 || weight[role] < weight[minimum] {
		writeError(c, http.StatusForbidden, "Forbidden")
		return false
	}
	return true
}

func nonEmptyPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func decode(c *gin.Context, destination any) bool {
	if err := c.ShouldBindJSON(destination); err != nil {
		writeError(c, http.StatusBadRequest, "Invalid request body")
		return false
	}
	return true
}

func decodeOptional(c *gin.Context, destination any) bool {
	if c.Request.ContentLength == 0 {
		return true
	}
	return decode(c, destination)
}

func writeData(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data, "success": true, "status": status})
}

func writeError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"statusCode": status, "message": message, "error": http.StatusText(status)})
}

func (handler *Handler) writeRepositoryError(c *gin.Context, err error, notFoundMessage string) {
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(c, http.StatusNotFound, notFoundMessage)
		return
	}
	writeError(c, http.StatusInternalServerError, "Database request failed")
}
