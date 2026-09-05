package http

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreparePublicPageContentRewritesFilesAndRemovesComments(t *testing.T) {
	handler := &Handler{tokens: newTokenService("test-secret")}
	content := []byte(`{"type":"doc","content":[{"type":"paragraph","marks":[{"type":"comment","attrs":{"commentId":"private"}},{"type":"bold"}],"content":[{"type":"image","attrs":{"attachmentId":"att-1","src":"/api/files/att-1/image.png"}},{"type":"attachment","attrs":{"attachmentId":"att-1","url":"/files/att-1/document.pdf?download=1"}}]}]}`)
	result, err := handler.preparePublicPageContent(content, "page-1", "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	value := string(result)
	if strings.Contains(value, `"type":"comment"`) {
		t.Fatalf("comment mark leaked into public content: %s", value)
	}
	if !strings.Contains(value, "/api/files/public/att-1/image.png") || !strings.Contains(value, "/files/public/att-1/document.pdf") || !strings.Contains(value, "jwt=") {
		t.Fatalf("attachment URLs were not rewritten: %s", value)
	}
	var document map[string]any
	if err := json.Unmarshal(result, &document); err != nil || document["type"] != "doc" {
		t.Fatalf("invalid rewritten document: %s", result)
	}
}

func TestPublicAttachmentURLKeepsExistingQuery(t *testing.T) {
	result := publicAttachmentURL("/api/files/id/file.pdf?download=1", "token")
	if !strings.Contains(result, "download=1") || !strings.Contains(result, "jwt=token") || !strings.HasPrefix(result, "/api/files/public/") {
		t.Fatalf("unexpected public URL: %s", result)
	}
}
