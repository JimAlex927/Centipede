package http

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"centipede/internal/modules/docmost/domain"
)

func TestBuildPagesZip(t *testing.T) {
	rootTitle, childTitle := "Root", "Child"
	pages := []domain.Page{
		{ID: "child", Title: &childTitle, ParentPageID: stringPointer("root"), Content: []byte(`{"type":"doc","content":[]}`)},
		{ID: "root", Title: &rootTitle, Content: []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}`)},
	}
	data, err := buildPagesZip(pages, "markdown", "root")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 2 {
		t.Fatalf("unexpected archive: files=%d err=%v", len(archive.File), err)
	}
	file, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(file)
	file.Close()
	if !strings.Contains(string(content), "hello") {
		t.Fatalf("export content missing: %s", content)
	}
}

func TestBuildDocx(t *testing.T) {
	title := "Export"
	data, err := buildDocx(domain.Page{Title: &title, Content: []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 5 {
		t.Fatalf("unexpected docx: files=%d err=%v", len(archive.File), err)
	}
}

func TestExportAttachmentsAreEmbeddedAndRewritten(t *testing.T) {
	content := []byte(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"image","attrs":{"attachmentId":"att-1","src":"/api/files/att-1/image.png"}}]}]}`)
	title := "Root"
	data, err := buildPagesZipWithAttachments([]domain.Page{{ID: "root", Title: &title, Content: content}}, "html", "root", []zipExportAttachment{{Attachment: domain.Attachment{ID: "att-1", FileName: "image.png"}, Data: []byte("image")}})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var htmlContent string
	var attachmentContent string
	for _, file := range archive.File {
		opened, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		value, readErr := io.ReadAll(opened)
		opened.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if file.Name == "Root.html" {
			htmlContent = string(value)
		}
		if file.Name == "files/att-1/image.png" {
			attachmentContent = string(value)
		}
	}
	if !strings.Contains(htmlContent, `src="files/att-1/image.png"`) || attachmentContent != "image" {
		t.Fatalf("attachment was not embedded and rewritten: html=%q attachment=%q", htmlContent, attachmentContent)
	}
}

func TestExportAttachmentIDs(t *testing.T) {
	ids := exportAttachmentIDs([]byte(`{"type":"doc","content":[{"type":"image","attrs":{"attachmentId":"one"}},{"type":"image","attrs":{"attachmentId":"one"}},{"type":"video","attrs":{"attachmentId":"two"}}]}`))
	if len(ids) != 2 || ids[0] != "one" || ids[1] != "two" {
		t.Fatalf("unexpected attachment ids: %#v", ids)
	}
}

func stringPointer(value string) *string { return &value }
