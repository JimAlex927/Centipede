package http

import (
	"strings"
	"testing"
)

func TestAddZipParentDirectories(t *testing.T) {
	directories := map[string]bool{}
	addZipParentDirectories(directories, "docs/team")
	if !directories["docs/team"] || !directories["docs"] || len(directories) != 2 {
		t.Fatalf("unexpected directories: %#v", directories)
	}
}

func TestImportTaskUUID(t *testing.T) {
	value, err := importTaskUUID()
	if err != nil || len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		t.Fatalf("invalid task uuid %q: %v", value, err)
	}
}

func TestZipResourcePath(t *testing.T) {
	if got := zipResourcePath("docs/page.md", "../images/diagram.png?download=1"); got != "images/diagram.png" {
		t.Fatalf("unexpected resource path: %q", got)
	}
	if got := zipResourcePath("docs/page.md", "https://example.com/image.png"); got != "" {
		t.Fatalf("external resource should not be imported: %q", got)
	}
}

func TestMarkdownImageImport(t *testing.T) {
	nodes := markdownInline("before ![diagram](images/diagram.png) after")
	if len(nodes) != 3 || nodes[1].Type != "image" || nodes[1].Attrs["src"] != "images/diagram.png" {
		t.Fatalf("markdown image was not parsed: %#v", nodes)
	}
	if !strings.Contains(importPlainText(nodes), "before ") {
		t.Fatalf("plain text fallback changed unexpectedly: %#v", nodes)
	}
}
