package http

import (
	"archive/zip"
	"bytes"
	"reflect"
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

func TestMergeNotionFolderPages(t *testing.T) {
	entries := []zipImportEntry{{Path: "Project 0123456789abcdef0123456789abcdef.md"}}
	mergeNotionFolderPages(&entries, []string{"Project"})
	if entries[0].Path != "Project.md" {
		t.Fatalf("Notion file was not associated with its folder page: %q", entries[0].Path)
	}

	entries = []zipImportEntry{{Path: "Project 0123456789abcdef0123456789abcdef.md"}}
	mergeNotionFolderPages(&entries, []string{"Project 0123-cdef"})
	if entries[0].Path != "Project 0123-cdef.md" {
		t.Fatalf("Notion partial UUID folder was not matched: %q", entries[0].Path)
	}

	entries = []zipImportEntry{{Path: "Project ffffffffffffffffffffffffffffffff.md"}}
	mergeNotionFolderPages(&entries, []string{"Project dead-beef"})
	if entries[0].Path != "Project ffffffffffffffffffffffffffffffff.md" {
		t.Fatalf("Notion partial UUID matched the wrong page: %q", entries[0].Path)
	}
}

func TestSingleZipRootDirectory(t *testing.T) {
	if !isSingleZipRootDirectory("export", []string{"export", "export/docs"}, []zipImportEntry{{Path: "export/page.md"}}) {
		t.Fatal("single ZIP root directory was not detected")
	}
	if isSingleZipRootDirectory("export", []string{"export"}, []zipImportEntry{{Path: "page.md"}}) {
		t.Fatal("root directory was incorrectly skipped when a root file exists")
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

func TestMarkdownInternalLinkImport(t *testing.T) {
	nodes := markdownInline("See [the guide](guide.md) for details")
	if len(nodes) != 3 || len(nodes[1].Marks) != 1 || nodes[1].Marks[0].Type != "link" {
		t.Fatalf("markdown link was not parsed: %#v", nodes)
	}
	pageIDs := map[string]string{"docs/guide.md": "guide-page"}
	rewriteZipInternalLinks(&nodes, "docs/index.md", "general", pageIDs)
	mark := nodes[1].Marks[0]
	if mark.Attrs["href"] != "/s/general/p/guide-page" || mark.Attrs["internal"] != true {
		t.Fatalf("internal link was not rewritten: %#v", mark)
	}
}

func TestZipEncodedPathAndMetadataLookup(t *testing.T) {
	icon := "📘"
	metadata := &zipImportMetadata{Pages: map[string]zipImportPageMetadata{
		"docs/My%20Page.md": {Position: "a1", Icon: &icon},
	}}
	value := zipImportPageMetadataForPath(metadata, "docs/My Page.md")
	if value.Position != "a1" || value.Icon == nil || *value.Icon != icon {
		t.Fatalf("encoded metadata path was not resolved: %#v", value)
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

func TestFilterConfluencePageEntriesRemovesRootIndex(t *testing.T) {
	entries := filterConfluencePageEntries([]zipImportEntry{
		{Path: "index.html"},
		{Path: "pages/12345/Project.html"},
	})
	if len(entries) != 1 || entries[0].Path != "pages/12345/Project.html" {
		t.Fatalf("Confluence root index was not removed: %#v", entries)
	}
	entries = filterConfluencePageEntries([]zipImportEntry{{Path: "index.html"}})
	if len(entries) != 1 {
		t.Fatal("single-page archive should retain its only document")
	}
}

func TestConfluenceDrawioPair(t *testing.T) {
	assets := map[string]*zip.File{
		"attachments/diagram.drawio": {},
		"attachments/diagram.png":    {},
	}
	drawio, png, ok := confluenceDrawioPair("attachments/diagram.png", assets)
	if !ok || drawio != "attachments/diagram.drawio" || png != "attachments/diagram.png" {
		t.Fatalf("unexpected Draw.io pair: %q, %q, %v", drawio, png, ok)
	}
	if drawio, png, ok = confluenceDrawioPair("attachments/diagram.drawio", assets); !ok || drawio != "attachments/diagram.drawio" || png != "attachments/diagram.png" {
		t.Fatalf("unexpected reverse Draw.io pair: %q, %q, %v", drawio, png, ok)
	}
	if _, _, ok = confluenceDrawioPair("attachments/other.png", assets); ok {
		t.Fatal("unrelated PNG was treated as a Draw.io pair")
	}
}

func TestConfluenceDrawioPairDetectsNumericServerFiles(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	drawio, err := writer.Create("attachments/123/45678")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drawio.Write([]byte("<?xml version=\"1.0\"?><mxfile><diagram/></mxfile>")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Create("attachments/123/45690.png"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string]*zip.File{}
	for _, file := range archive.File {
		assets[file.Name] = file
	}
	gotDrawio, gotPNG, ok := confluenceDrawioPair("attachments/123/45678", assets)
	if !ok || gotDrawio != "attachments/123/45678" || gotPNG != "attachments/123/45690.png" {
		t.Fatalf("numeric Confluence Draw.io source was not detected: %q, %q, %v", gotDrawio, gotPNG, ok)
	}
	gotDrawio, gotPNG, ok = confluenceDrawioPair("attachments/123/45690.png", assets)
	if !ok || gotDrawio != "attachments/123/45678" || gotPNG != "attachments/123/45690.png" {
		t.Fatalf("numeric Confluence Draw.io preview was not paired: %q, %q, %v", gotDrawio, gotPNG, ok)
	}
}

func TestResolveConfluenceAssetPathSupportsNumericAliases(t *testing.T) {
	assets := map[string]*zip.File{
		"attachments/123/45678":      {},
		"attachments/123/manual.pdf": {},
	}
	for input, want := range map[string]string{
		"attachments/123/45678":             "attachments/123/45678",
		"attachments/123/45678/diagram.png": "attachments/123/45678",
		"attachments/123/45678.png":         "attachments/123/45678",
		"attachments/123/manual.pdf":        "attachments/123/manual.pdf",
		"attachments/123/MANUAL.pdf":        "attachments/123/manual.pdf",
		"attachments/123/missing-image.png": "attachments/123/missing-image.png",
	} {
		if got := resolveConfluenceAssetPath(input, assets); got != want {
			t.Fatalf("resolveConfluenceAssetPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConfluencePageAttachmentPaths(t *testing.T) {
	assets := map[string]*zip.File{
		"attachments/12345/manual.pdf":     {},
		"attachments/12345/diagram.drawio": {},
		"attachments/12345/diagram.png":    {},
		"attachments/99999/other.pdf":      {},
		"images/logo.png":                  {},
	}
	got := confluencePageAttachmentPaths("pages/12345/Project%20plan.html", assets)
	want := []string{
		"attachments/12345/diagram.drawio",
		"attachments/12345/diagram.png",
		"attachments/12345/manual.pdf",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected page attachment paths: %#v", got)
	}
	if got := confluencePageIDs("index.html"); len(got) != 0 {
		t.Fatalf("index page unexpectedly had Confluence page IDs: %#v", got)
	}
}

func TestConfluenceAttachmentNamePreservesHTMLFilename(t *testing.T) {
	if got := confluenceAttachmentName("/download/attachments/123/45678/报告.pdf", "attachments/123/45678", "confluence"); got != "报告.pdf" {
		t.Fatalf("unexpected aliased attachment name: %q", got)
	}
	if got := confluenceAttachmentName("attachments/123/45678", "attachments/123/45678", "confluence"); got != "45678" {
		t.Fatalf("unexpected numeric attachment name: %q", got)
	}
	if got := confluenceAttachmentName("images/manual.pdf", "images/manual.pdf", "generic"); got != "manual.pdf" {
		t.Fatalf("unexpected generic attachment name: %q", got)
	}
}

func TestBuildDrawioSVG(t *testing.T) {
	value := buildDrawioSVG([]byte("<mxfile/>"), []byte("png"))
	if !bytes.Contains(value, []byte(`content="PG14ZmlsZS8+"`)) || !bytes.Contains(value, []byte(`data:image/png;base64,cG5n`)) {
		t.Fatalf("Draw.io SVG does not contain embedded data: %s", value)
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
