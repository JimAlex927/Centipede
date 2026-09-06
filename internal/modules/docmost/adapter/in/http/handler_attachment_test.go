package http

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"centipede/internal/modules/docmost/adapter/out/storage"
	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

func TestServeAttachmentAllowsInlinePDFPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localStorage, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const filePath = "workspace/files/attachment/manual.pdf"
	if _, err = localStorage.Save(context.Background(), filePath, bytes.NewReader([]byte("%PDF-1.7")), 1024); err != nil {
		t.Fatal(err)
	}

	mimeType := "application/pdf"
	attachmentType := "file"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/files/attachment/manual.pdf", nil)
	context.Writer.Header().Set("X-Frame-Options", "DENY")

	handler := &Handler{storage: localStorage}
	handler.serveAttachment(context, domain.Attachment{
		FileName:  "manual.pdf",
		FilePath:  filePath,
		FileExt:   ".pdf",
		MimeType:  &mimeType,
		Type:      &attachmentType,
		UpdatedAt: time.Now().UTC(),
	}, false)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want header removed for inline preview", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != mimeType {
		t.Fatalf("Content-Type = %q, want %q", got, mimeType)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want no download disposition for PDF", got)
	}
}

func TestServeAttachmentAllowsInlineSVGPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localStorage, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const filePath = "workspace/files/attachment/diagram.drawio.svg"
	if _, err = localStorage.Save(context.Background(), filePath, bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="10" height="10"/></svg>`)), 1024); err != nil {
		t.Fatal(err)
	}

	mimeType := "image/svg+xml"
	attachmentType := "file"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/files/attachment/diagram.drawio.svg", nil)

	handler := &Handler{storage: localStorage}
	handler.serveAttachment(context, domain.Attachment{
		FileName:  "diagram.drawio.svg",
		FilePath:  filePath,
		FileExt:   ".svg",
		MimeType:  &mimeType,
		Type:      &attachmentType,
		UpdatedAt: time.Now().UTC(),
	}, false)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != mimeType {
		t.Fatalf("Content-Type = %q, want %q", got, mimeType)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want no download disposition for SVG", got)
	}
}

func TestServeAttachmentRepairsLegacySVGContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localStorage, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const filePath = "workspace/files/attachment/legacy.drawio.svg"
	if _, err = localStorage.Save(context.Background(), filePath, bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), 1024); err != nil {
		t.Fatal(err)
	}

	legacyMimeType := "text/xml"
	attachmentType := "file"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/files/attachment/legacy.drawio.svg", nil)

	handler := &Handler{storage: localStorage}
	handler.serveAttachment(context, domain.Attachment{
		FileName:  "legacy.drawio.svg",
		FilePath:  filePath,
		FileExt:   ".svg",
		MimeType:  &legacyMimeType,
		Type:      &attachmentType,
		UpdatedAt: time.Now().UTC(),
	}, false)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("Content-Type = %q, want image/svg+xml", got)
	}
}

func TestServeAttachmentTreatsCompositeSVGExtensionAsInline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localStorage, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const filePath = "workspace/files/attachment/diagram.drawio.svg"
	if _, err = localStorage.Save(context.Background(), filePath, bytes.NewReader([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), 1024); err != nil {
		t.Fatal(err)
	}

	legacyMimeType := "text/xml"
	attachmentType := "file"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/files/attachment/diagram.drawio.svg", nil)

	handler := &Handler{storage: localStorage}
	handler.serveAttachment(context, domain.Attachment{
		FileName:  "diagram.drawio.svg",
		FilePath:  filePath,
		FileExt:   ".drawio.svg",
		MimeType:  &legacyMimeType,
		Type:      &attachmentType,
		UpdatedAt: time.Now().UTC(),
	}, false)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("Content-Type = %q, want image/svg+xml", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("Content-Disposition = %q, want no download disposition for SVG", got)
	}
}
