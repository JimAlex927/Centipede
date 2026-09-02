package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"centipede/internal/modules/identity/application"
	"centipede/internal/modules/identity/domain"

	"github.com/gin-gonic/gin"
)

type fakeAuthenticator struct{}

func (fakeAuthenticator) Register(context.Context, string, string, string) (application.AuthResult, error) {
	return application.AuthResult{}, nil
}
func (fakeAuthenticator) Login(context.Context, string, string) (application.AuthResult, error) {
	return application.AuthResult{}, nil
}
func (fakeAuthenticator) Refresh(context.Context, string) (application.AuthResult, error) {
	return application.AuthResult{}, nil
}
func (fakeAuthenticator) Logout(context.Context, string) error { return nil }
func (fakeAuthenticator) CurrentUser(_ context.Context, token string) (domain.User, error) {
	if token != "valid" {
		return domain.User{}, application.ErrInvalidCredentials
	}
	return domain.User{ID: 9, Email: "reader@example.com", Status: "active"}, nil
}

func TestMiddlewareRequiresBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(fakeAuthenticator{}, time.Minute, time.Hour, "refresh", false)
	router := gin.New()
	router.GET("/private", handler.Middleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	withoutToken := httptest.NewRecorder()
	router.ServeHTTP(withoutToken, httptest.NewRequest(http.MethodGet, "/private", nil))
	if withoutToken.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", withoutToken.Code)
	}

	withToken := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	request.Header.Set("Authorization", "Bearer valid")
	router.ServeHTTP(withToken, request)
	if withToken.Code != http.StatusNoContent {
		t.Fatalf("expected private route, got %d", withToken.Code)
	}
}
