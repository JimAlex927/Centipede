package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeadersAllowShareAndAttachmentEmbedding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(securityHeaders())
	router.GET("/share/:shareID/p/:pageSlug", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/files/:fileID/:fileName", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/api/pages/:pageID", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/share/share-1/p/home", "/api/files/file-1/diagram.svg"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("X-Frame-Options"); got != "" {
			t.Fatalf("%s: X-Frame-Options = %q, want omitted", path, got)
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/pages/page-1", nil))
	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("normal page: X-Frame-Options = %q, want DENY", got)
	}
}
