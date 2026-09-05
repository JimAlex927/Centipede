package http

import (
	"reflect"
	"strings"
	"testing"

	"centipede/internal/modules/docmost/domain"
)

func TestParseMarkdownImport(t *testing.T) {
	nodes, err := parseMarkdown(strings.NewReader("# Imported\n\nHello **world**.\n\n- one\n- two"))
	if err != nil {
		t.Fatal(err)
	}
	title, nodes := extractImportedTitle(nodes, "fallback")
	if title != "Imported" || len(nodes) != 2 || nodes[0].Type != "paragraph" || nodes[1].Type != "bulletList" {
		t.Fatalf("unexpected markdown import: title=%q nodes=%#v", title, nodes)
	}
	if len(nodes[0].Content) != 3 || nodes[0].Content[1].Marks[0].Type != "bold" {
		t.Fatalf("inline marks were not preserved: %#v", nodes[0].Content)
	}
}

func TestParseMarkdownLiteralHashDoesNotLoop(t *testing.T) {
	nodes, err := parseMarkdown(strings.NewReader("#not a heading"))
	if err != nil || len(nodes) != 1 || nodes[0].Type != "paragraph" {
		t.Fatalf("unexpected literal hash parsing: nodes=%#v err=%v", nodes, err)
	}
}

func TestParseHTMLImport(t *testing.T) {
	nodes, err := parseImportedDocument(strings.NewReader("<html><body><h2>Title</h2><p>Hello <strong>world</strong>.</p><ul><li>one</li></ul></body></html>"), ".html")
	if err != nil {
		t.Fatal(err)
	}
	title, nodes := extractImportedTitle(nodes, "fallback")
	if title != "Title" || len(nodes) != 2 || nodes[0].Type != "paragraph" || nodes[1].Type != "bulletList" {
		t.Fatalf("unexpected html import: title=%q nodes=%#v", title, nodes)
	}
	want := []importMark{{Type: "bold"}}
	if !reflect.DeepEqual(nodes[0].Content[1].Marks, want) {
		t.Fatalf("unexpected html marks: %#v", nodes[0].Content[1].Marks)
	}
}

func TestParseDocxImport(t *testing.T) {
	title := "DOCX title"
	data, err := buildDocx(domain.Page{Title: &title, Content: []byte(`{"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Section"}]},{"type":"paragraph","content":[{"type":"text","text":"Hello ","marks":[{"type":"bold"}]},{"type":"text","text":"world"}]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := parseImportedDocument(strings.NewReader(string(data)), ".docx")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 || nodes[0].Type != "heading" || importPlainText(nodes[0].Content) != "DOCX title" || nodes[1].Type != "heading" || nodes[2].Type != "paragraph" {
		t.Fatalf("unexpected docx nodes: %#v", nodes)
	}
	if len(nodes[2].Content) != 2 || len(nodes[2].Content[0].Marks) != 1 || nodes[2].Content[0].Marks[0].Type != "bold" {
		t.Fatalf("docx marks were not preserved: %#v", nodes[2].Content)
	}
}
