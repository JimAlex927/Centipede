package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSharePageRedirectUsesStandaloneFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{frontendURL: "http://frontend.example"}
	router := gin.New()
	router.GET("/share/:shareID/p/:pageSlug", handler.sharePageRedirect)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/share/key/p/page-title-123?ref=mail", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect {
		t.Fatalf("expected 307, got %d", response.Code)
	}
	if got := response.Header().Get("Location"); got != "http://frontend.example/share/key/p/page-title-123?ref=mail" {
		t.Fatalf("unexpected redirect location: %q", got)
	}
}

func TestSharePageRedirectRequiresFrontendURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	router := gin.New()
	router.GET("/share/p/:pageSlug", handler.sharePageRedirect)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/share/p/page-title-123", nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without frontend URL, got %d", response.Code)
	}
}
