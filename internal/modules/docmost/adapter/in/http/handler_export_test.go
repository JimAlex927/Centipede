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

func stringPointer(value string) *string { return &value }
