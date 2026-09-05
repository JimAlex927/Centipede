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
	if got := zipResourcePathForSource("Page.html", "/download/attachments/diagram.png?version=1", "confluence"); got != "attachments/diagram.png" {
		t.Fatalf("unexpected Confluence resource path: %q", got)
	}
}

func TestZipImportTitleStripsNotionIDs(t *testing.T) {
	if got := zipImportTitle("Project plan 0123456789abcdef0123456789abcdef", "notion"); got != "Project plan" {
		t.Fatalf("Notion page id was not stripped: %q", got)
	}
	if got := zipImportTitle("Project plan abcd-1234", "notion"); got != "Project plan" {
		t.Fatalf("Notion partial page id was not stripped: %q", got)
	}
	if got := zipImportTitle("Project plan 0123456789abcdef0123456789abcdef", "generic"); got == "Project plan" {
		t.Fatal("generic import unexpectedly stripped a title suffix")
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

func TestZipImportSources(t *testing.T) {
	for _, source := range []string{"generic", "notion", "confluence"} {
		if !isSupportedZipImportSource(source) {
			t.Fatalf("source unexpectedly rejected: %q", source)
		}
	}
	if isSupportedZipImportSource("unknown") {
		t.Fatal("unknown source unexpectedly accepted")
	}
}

func TestSupportedZipDocumentExtensions(t *testing.T) {
	for _, extension := range []string{".md", ".html", ".docx", ".pdf", ".csv", ".DOCX"} {
		if !isSupportedZipDocumentExtension(extension) {
			t.Fatalf("supported document extension rejected: %q", extension)
		}
	}
	if isSupportedZipDocumentExtension(".png") {
		t.Fatal("non-document ZIP entry unexpectedly treated as a page")
	}
}

func TestCSVImport(t *testing.T) {
	nodes, err := parseCSV(strings.NewReader("Name,Role\nAlice,Writer\nBob,Reader\n"))
	if err != nil || len(nodes) != 1 || nodes[0].Type != "table" || len(nodes[0].Content) != 3 {
		t.Fatalf("unexpected CSV nodes: %#v, %v", nodes, err)
	}
	if nodes[0].Content[0].Content[0].Type != "tableHeader" || importPlainText(nodes[0].Content[1].Content[0].Content) != "Alice" {
		t.Fatalf("CSV table structure was not preserved: %#v", nodes[0])
	}
}
