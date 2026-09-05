package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPDFRenderRejectsTokenForAnotherPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{tokens: newTokenService("test-secret")}
	token, err := handler.tokens.issue(tokenClaims{
		PageID:      "page-1",
		WorkspaceID: "workspace-1",
		Type:        "pdf_render",
	}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/pdf-export/render", strings.NewReader(`{"pageId":"page-2","token":"`+token+`"}`))
	context.Request.Header.Set("Content-Type", "application/json")
	handler.pdfRender(context)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}
