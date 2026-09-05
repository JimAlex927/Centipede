package http

import (
	"reflect"
	"strings"
	"testing"
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
