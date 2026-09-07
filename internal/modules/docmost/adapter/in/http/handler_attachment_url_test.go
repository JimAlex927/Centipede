package http

import (
	"testing"

	"centipede/internal/modules/docmost/domain"
)

func TestFileURLUsesRelativeAttachmentPath(t *testing.T) {
	got := (&Handler{publicURL: "http://localhost:8080"}).fileURL(domain.Attachment{
		ID:       "attachment-1",
		FileName: "diagram.excalidraw.svg",
	})

	if got != "/api/files/attachment-1/diagram.excalidraw.svg" {
		t.Fatalf("file URL = %q", got)
	}
}
