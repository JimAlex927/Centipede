package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"centipede/internal/modules/identity/application"
	"centipede/internal/modules/identity/domain"

	"github.com/gin-gonic/gin"
)

type Authenticator interface {
	Register(context.Context, string, string, string) (application.AuthResult, error)
	Login(context.Context, string, string) (application.AuthResult, error)
	Refresh(context.Context, string) (application.AuthResult, error)
	Logout(context.Context, string) error
	CurrentUser(context.Context, string) (domain.User, error)
}

type Handler struct {
	auth          Authenticator
	accessTTL     time.Duration
	refreshTTL    time.Duration
	refreshCookie string
	cookieSecure  bool
}

func NewHandler(auth Authenticator, accessTTL, refreshTTL time.Duration, refreshCookie string, cookieSecure bool) *Handler {
	return &Handler{auth: auth, accessTTL: accessTTL, refreshTTL: refreshTTL, refreshCookie: refreshCookie, cookieSecure: cookieSecure}
}

type credentialsRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type refreshResponse struct {
	User        userResponse `json:"user"`
	AccessToken string       `json:"access_token"`
	ExpiresAt   time.Time    `json:"expires_at"`
}

type userResponse struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

func (handler *Handler) Register(router gin.IRouter) {
	group := router.Group("/api/v1/auth")
	group.POST("/register", handler.register)
	group.POST("/login", handler.login)
	group.POST("/refresh", handler.refresh)
	group.POST("/logout", handler.logout)
	group.GET("/me", handler.me)
}

func (handler *Handler) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		value, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			writeError(c, http.StatusUnauthorized, "unauthorized", "authentication is required")
			c.Abort()
			return
		}
		id, err := handler.auth.CurrentUser(c.Request.Context(), value)
		if err != nil {
			writeError(c, http.StatusUnauthorized, "unauthorized", "access token is invalid or expired")
			c.Abort()
			return
		}
		c.Set("user_id", id.ID)
		c.Set("current_user", id)
		c.Next()
	}
}

func (handler *Handler) register(c *gin.Context) {
	var request credentialsRequest
	if !decodeJSON(c, &request) {
		return
	}
	result, err := handler.auth.Register(c.Request.Context(), request.Email, request.Password, request.DisplayName)
	if err != nil {
		handler.writeAuthError(c, err)
		return
	}
	handler.writeAuth(c, result)
}

func (handler *Handler) login(c *gin.Context) {
	var request credentialsRequest
	if !decodeJSON(c, &request) {
		return
	}
	result, err := handler.auth.Login(c.Request.Context(), request.Email, request.Password)
	if err != nil {
		handler.writeAuthError(c, err)
		return
	}
	handler.writeAuth(c, result)
}

func (handler *Handler) refresh(c *gin.Context) {
	result, err := handler.auth.Refresh(c.Request.Context(), handler.refreshToken(c))
	if err != nil {
		writeError(c, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid or expired")
		return
	}
	handler.writeAuth(c, result)
}

func (handler *Handler) logout(c *gin.Context) {
	if err := handler.auth.Logout(c.Request.Context(), handler.refreshToken(c)); err != nil {
		writeError(c, http.StatusInternalServerError, "logout_failed", "could not end the session")
		return
	}
	handler.clearRefreshCookie(c)
	c.Status(http.StatusNoContent)
}

func (handler *Handler) me(c *gin.Context) {
	value, ok := bearerToken(c.GetHeader("Authorization"))
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "authentication is required")
		return
	}
	user, err := handler.auth.CurrentUser(c.Request.Context(), value)
	if err != nil {
		writeError(c, http.StatusUnauthorized, "unauthorized", "access token is invalid or expired")
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(user)})
}

func (handler *Handler) refreshToken(c *gin.Context) string {
	cookie, err := c.Request.Cookie(handler.refreshCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (handler *Handler) writeAuth(c *gin.Context, result application.AuthResult) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: handler.refreshCookie, Value: result.RefreshToken, Path: "/api/v1/auth",
		MaxAge: int(handler.refreshTTL.Seconds()), HttpOnly: true, Secure: handler.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	c.JSON(http.StatusOK, refreshResponse{User: toUserResponse(result.User), AccessToken: result.AccessToken, ExpiresAt: result.ExpiresAt})
}

func (handler *Handler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: handler.refreshCookie, Value: "", Path: "/api/v1/auth", MaxAge: -1, HttpOnly: true, Secure: handler.cookieSecure, SameSite: http.SameSiteLaxMode})
}

func (handler *Handler) writeAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidCredentials):
		writeError(c, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	case errors.Is(err, application.ErrEmailAlreadyExists):
		writeError(c, http.StatusConflict, "email_already_exists", "an account with this email already exists")
	default:
		if err.Error() == "email is invalid" || err.Error() == "password must contain 8 to 128 characters" || err.Error() == "display name is too long" {
			writeError(c, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeError(c, http.StatusInternalServerError, "auth_failed", "authentication request failed")
	}
}

func toUserResponse(user domain.User) userResponse {
	return userResponse{ID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Status: user.Status}
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", false
	}
	return header[len(prefix):], true
}

func decodeJSON(c *gin.Context, destination any) bool {
	if err := c.ShouldBindJSON(destination); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_json", "request body is invalid")
		return false
	}
	return true
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func userID(c *gin.Context) (int64, bool) {
	value, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	id, ok := value.(int64)
	return id, ok && id > 0
}

func parseID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}
