package http

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type invitationRequest struct {
	InvitationID string   `json:"invitationId"`
	Emails       []string `json:"emails"`
	GroupIDs     []string `json:"groupIds"`
	Role         string   `json:"role"`
	Name         string   `json:"name"`
	Password     string   `json:"password"`
	Token        string   `json:"token"`
	Query        string   `json:"query"`
	Limit        int      `json:"limit"`
}

func (handler *Handler) invitations(c *gin.Context) {
	var request invitationRequest
	if !decodeOptional(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	result, err := handler.repository.Invitations(c.Request.Context(), current.Workspace.ID, request.Query, request.Limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load invitations")
		return
	}
	writeData(c, http.StatusOK, result)
}

func (handler *Handler) invitationInfo(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	if request.InvitationID == "" {
		writeError(c, http.StatusBadRequest, "Invitation id is required")
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	invitation, err := handler.repository.InvitationByID(c.Request.Context(), request.InvitationID, workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Invitation not found")
		return
	}
	invitation.EnforceSSO = workspace.EnforceSSO
	writeData(c, http.StatusOK, invitation)
}

func (handler *Handler) createInvitations(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	created, err := handler.repository.CreateInvitations(c.Request.Context(), current.Workspace.ID, current.User.ID, request.Role, request.Emails, request.GroupIDs)
	if err != nil {
		handler.logger.Error("create invitations failed",
			zap.String("request_id", c.GetString("request_id")),
			zap.String("workspace_id", current.Workspace.ID),
			zap.String("user_id", current.User.ID),
			zap.String("role", request.Role),
			zap.Int("email_count", len(request.Emails)),
			zap.Int("group_count", len(request.GroupIDs)),
			zap.Error(err),
		)
		if errors.Is(err, postgres.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "Invalid invitation request")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to create invitations")
		}
		return
	}
	for _, invitation := range created {
		handler.sendInvitationEmail(c, invitation)
	}
	writeData(c, http.StatusOK, created)
}

func (handler *Handler) resendInvitation(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	invitation, err := handler.repository.InvitationByID(c.Request.Context(), request.InvitationID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Invitation not found")
		return
	}
	handler.sendInvitationEmail(c, invitation)
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) revokeInvitation(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	if err := handler.repository.RevokeInvitation(c.Request.Context(), request.InvitationID, current.Workspace.ID); err != nil {
		handler.writeRepositoryError(c, err, "Invitation not found")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) invitationLink(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	current := currentPrincipal(c)
	if !isAdmin(current.User) {
		writeError(c, http.StatusForbidden, "Forbidden")
		return
	}
	token, err := handler.repository.InvitationToken(c.Request.Context(), request.InvitationID, current.Workspace.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Invitation not found")
		return
	}
	link := handler.publicFrontendURL(c) + "/invites/" + url.PathEscape(request.InvitationID) + "?token=" + url.QueryEscape(token)
	writeData(c, http.StatusOK, gin.H{"inviteLink": link})
}

func (handler *Handler) acceptInvitation(c *gin.Context) {
	var request invitationRequest
	if !decode(c, &request) {
		return
	}
	if len(strings.TrimSpace(request.Name)) < 2 || len(request.Password) < 8 || request.Token == "" || request.InvitationID == "" {
		writeError(c, http.StatusBadRequest, "Name, invitation token and password of at least 8 characters are required")
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		handler.writeRepositoryError(c, err, "Workspace not found")
		return
	}
	if workspace.EnforceSSO {
		writeError(c, http.StatusBadRequest, "This workspace has enforced SSO login.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to secure password")
		return
	}
	user, err := handler.repository.AcceptInvitation(c.Request.Context(), request.InvitationID, request.Token, strings.TrimSpace(request.Name), string(hash), workspace.ID, handler.licenseSeatLimit(c.Request.Context(), workspace.ID))
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusBadRequest, "Invalid invitation or token")
		} else if errors.Is(err, postgres.ErrEmailDomainNotAllowed) {
			writeError(c, http.StatusBadRequest, "The invitation email domain is not approved for this workspace")
		} else if errors.Is(err, postgres.ErrLicenseSeatsExceeded) {
			writeError(c, http.StatusConflict, "The active license seat limit has been reached")
		} else {
			writeError(c, http.StatusBadRequest, "Failed to accept invitation")
		}
		return
	}
	if workspace.EnforceMFA {
		writeData(c, http.StatusOK, gin.H{"requiresLogin": true})
		return
	}
	if err := handler.startSession(c, user); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
	writeData(c, http.StatusOK, gin.H{"requiresLogin": false})
}

type passwordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
	Email       string `json:"email"`
	Token       string `json:"token"`
	Type        string `json:"type"`
}

func (handler *Handler) changePassword(c *gin.Context) {
	var request passwordRequest
	if !decode(c, &request) {
		return
	}
	if len(request.OldPassword) < 8 || len(request.NewPassword) < 8 {
		writeError(c, http.StatusBadRequest, "Passwords must be at least 8 characters")
		return
	}
	current := currentPrincipal(c)
	if current.User.Password == nil || bcrypt.CompareHashAndPassword([]byte(*current.User.Password), []byte(request.OldPassword)) != nil {
		writeError(c, http.StatusBadRequest, "Current password is incorrect")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to secure password")
		return
	}
	if err = handler.repository.ChangePassword(c.Request.Context(), current.User.ID, current.Workspace.ID, string(hash), current.SessionID); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to change password")
		return
	}
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) forgotPassword(c *gin.Context) {
	var request passwordRequest
	if !decode(c, &request) {
		return
	}
	if !strings.Contains(request.Email, "@") {
		writeError(c, http.StatusBadRequest, "A valid email is required")
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		// Do not disclose whether the workspace or account exists.
		writeData(c, http.StatusOK, nil)
		return
	}
	if workspace.EnforceSSO {
		writeError(c, http.StatusBadRequest, "This workspace has enforced SSO login.")
		return
	}
	user, err := handler.repository.UserByEmail(c.Request.Context(), request.Email, workspace.ID)
	if err == nil && user.DeactivatedAt == nil && user.DeletedAt == nil {
		token, tokenErr := handler.repository.CreatePasswordResetToken(c.Request.Context(), user.ID, workspace.ID, time.Now().UTC().Add(30*time.Minute))
		if tokenErr == nil && handler.mailer != nil {
			link := handler.publicFrontendURL(c) + "/password-reset?token=" + url.QueryEscape(token)
			_ = handler.mailer.Send(c.Request.Context(), user.Email, "Reset your password", "Use this link to reset your password:\n\n"+link+"\n\nThis link expires in 30 minutes.")
		}
	}
	// Always return the same response to prevent account enumeration.
	writeData(c, http.StatusOK, nil)
}

func (handler *Handler) sendInvitationEmail(c *gin.Context, invitation domain.Invitation) {
	if handler.mailer == nil || strings.TrimSpace(invitation.Email) == "" {
		return
	}
	link := handler.publicFrontendURL(c) + "/invites/" + url.PathEscape(invitation.ID) + "?token=" + url.QueryEscape(invitation.Token)
	message := "You have been invited to join a Docmost workspace.\n\nAccept the invitation here:\n" + link + "\n\nYour assigned role: " + invitation.Role
	_ = handler.mailer.Send(c.Request.Context(), invitation.Email, "You are invited to Docmost", message)
}

func (handler *Handler) verifyUserToken(c *gin.Context) {
	var request passwordRequest
	if !decode(c, &request) {
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid or expired token")
		return
	}
	valid, err := handler.repository.PasswordResetTokenValid(c.Request.Context(), request.Token, request.Type, workspace.ID)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to verify token")
		return
	}
	if !valid {
		writeError(c, http.StatusBadRequest, "Invalid or expired token")
		return
	}
	writeData(c, http.StatusOK, gin.H{"valid": true})
}

func (handler *Handler) passwordReset(c *gin.Context) {
	var request passwordRequest
	if !decode(c, &request) {
		return
	}
	if len(request.NewPassword) < 8 || request.Token == "" {
		writeError(c, http.StatusBadRequest, "Invalid password reset request")
		return
	}
	workspace, err := handler.repository.OnlyWorkspace(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadRequest, "Invalid or expired token")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to secure password")
		return
	}
	user, err := handler.repository.ResetPassword(c.Request.Context(), request.Token, workspace.ID, string(hash))
	if err != nil {
		if errors.Is(err, postgres.ErrNotFound) {
			writeError(c, http.StatusBadRequest, "Invalid or expired token")
		} else {
			writeError(c, http.StatusInternalServerError, "Failed to reset password")
		}
		return
	}
	requiresMFA, mfaErr := handler.userRequiresMFA(c.Request.Context(), user.ID, workspace)
	if mfaErr != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load MFA settings")
		return
	}
	if requiresMFA {
		writeData(c, http.StatusOK, gin.H{"requiresLogin": true})
		return
	}
	if err := handler.startSession(c, user); err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create login session")
		return
	}
	writeData(c, http.StatusOK, gin.H{"requiresLogin": false})
}

func (handler *Handler) userRequiresMFA(ctx context.Context, userID string, workspace domain.Workspace) (bool, error) {
	record, err := handler.repository.MFAByUser(ctx, userID, workspace.ID)
	if err != nil && !errors.Is(err, postgres.ErrNotFound) {
		return false, err
	}
	return mfaRequired(record, err, workspace.EnforceMFA), nil
}

func mfaRequired(record postgres.MFARecord, lookupErr error, workspaceEnforces bool) bool {
	if lookupErr != nil && !errors.Is(lookupErr, postgres.ErrNotFound) {
		return false
	}
	return (lookupErr == nil && record.IsEnabled) ||
		(workspaceEnforces && (errors.Is(lookupErr, postgres.ErrNotFound) || !record.IsEnabled))
}

func (handler *Handler) publicFrontendURL(c *gin.Context) string {
	if handler.frontendURL != "" {
		return handler.frontendURL
	}
	if origin := strings.TrimRight(c.GetHeader("Origin"), "/"); origin != "" {
		return origin
	}
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
