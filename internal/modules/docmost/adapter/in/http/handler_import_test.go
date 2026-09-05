package http

import (
	"fmt"
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

func TestParseHTMLDocmostCustomNodes(t *testing.T) {
	html := `<html><body>
<div data-type="drawio" data-src="files/diagram.drawio.svg" data-title="diagram" data-width="600"></div>
<div data-type="attachment" data-attachment-url="files/manual.pdf" data-attachment-name="manual.pdf" data-attachment-mime="application/pdf"></div>
<video src="media/demo.mp4" width="720" data-attachment-id="video-1"></video>
<audio><source src="media/sound.mp3"></audio>
<div data-type="callout" data-callout-type="warning"><p>注意</p></div>
</body></html>`
	nodes, err := parseImportedDocument(strings.NewReader(html), ".html")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 {
		t.Fatalf("unexpected custom node count: %#v", nodes)
	}
	if nodes[0].Type != "drawio" || nodes[0].Attrs["src"] != "files/diagram.drawio.svg" {
		t.Fatalf("drawio node was not preserved: %#v", nodes[0])
	}
	if nodes[1].Type != "attachment" || nodes[1].Attrs["url"] != "files/manual.pdf" {
		t.Fatalf("attachment node was not preserved: %#v", nodes[1])
	}
	if nodes[2].Type != "video" || nodes[2].Attrs["src"] != "media/demo.mp4" {
		t.Fatalf("video node was not preserved: %#v", nodes[2])
	}
	if nodes[3].Type != "audio" || nodes[3].Attrs["src"] != "media/sound.mp3" {
		t.Fatalf("audio node was not preserved: %#v", nodes[3])
	}
	if nodes[4].Type != "callout" || len(nodes[4].Content) != 1 || importPlainText(nodes[4].Content) != "注意" {
		t.Fatalf("callout content was not preserved: %#v", nodes[4])
	}
}

func TestParseNotionHTMLSpecialNodes(t *testing.T) {
	html := `<html><body>
<header><span class="page-header-icon">📘</span><img class="page-cover-image" src="cover.png"></header>
<p class="page-description">   </p>
<div class="column-list"><div class="column"><div style="display:contents"><p>左侧</p></div></div><div class="column"><p>右侧</p></div></div>
<figure class="equation"><math><annotation encoding="application/x-tex">x^2</annotation></math></figure>
<p>面积 <span class="notion-text-equation-token"><math><annotation encoding="application/x-tex">a+b</annotation></math></span></p>
<figure class="callout"><div><span>💡</span></div><div><p>提示内容</p></div></figure>
<ul class="to-do-list"><li><span class="checkbox checkbox-on"></span><span class="to-do-children-checked">已完成</span></li><li><span class="checkbox"></span><span class="to-do-children-unchecked">待处理</span></li></ul>
</body></html>`

	nodes, err := parseImportedDocument(strings.NewReader(html), ".html")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 5 {
		t.Fatalf("unexpected Notion node count: %#v", nodes)
	}
	if nodes[0].Type != "columns" || len(nodes[0].Content) != 2 || nodes[0].Content[0].Type != "column" || importPlainText(nodes[0].Content[0].Content) != "左侧" || importPlainText(nodes[0].Content[1].Content) != "右侧" {
		t.Fatalf("Notion columns were not normalized: %#v", nodes[0])
	}
	if nodes[1].Type != "mathBlock" || nodes[1].Attrs["text"] != "x^2" {
		t.Fatalf("Notion block equation was not normalized: %#v", nodes[1])
	}
	if nodes[2].Type != "paragraph" || len(nodes[2].Content) != 2 || nodes[2].Content[1].Type != "mathInline" || nodes[2].Content[1].Attrs["text"] != "a+b" {
		t.Fatalf("Notion inline equation was not normalized: %#v", nodes[2])
	}
	if nodes[3].Type != "callout" || importPlainText(nodes[3].Content) != "提示内容" {
		t.Fatalf("Notion callout was not normalized: %#v", nodes[3])
	}
	if nodes[4].Type != "taskList" || len(nodes[4].Content) != 2 || nodes[4].Content[0].Type != "taskItem" || nodes[4].Content[0].Attrs["checked"] != true || nodes[4].Content[1].Attrs["checked"] != false || importPlainText(nodes[4].Content[0].Content) != "已完成" {
		t.Fatalf("Notion todo list was not normalized: %#v", nodes[4])
	}
}

func TestParseNotionHTMLDetailsAndDefaultEmbeds(t *testing.T) {
	html := `<html><body>
<ul class="toggle"><li><details open=""><summary>展开项</summary><p>折叠内容</p></details></li></ul>
<figure class="bookmark"><a class="bookmark source" href="https://example.com"><div class="bookmark-title">示例站点</div></a></figure>
<iframe src="https://example.com/embed"></iframe>
<nav class="table_of_contents"><a href="#x">目录</a></nav>
</body></html>`
	nodes, err := parseImportedDocument(strings.NewReader(html), ".html")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("unexpected normalized default node count: %#v", nodes)
	}
	if nodes[0].Type != "details" || nodes[0].Attrs["open"] != true || len(nodes[0].Content) != 2 || importPlainText(nodes[0].Content[0].Content) != "展开项" || importPlainText(nodes[0].Content[1].Content) != "折叠内容" {
		t.Fatalf("details were not preserved: %#v", nodes[0])
	}
	if nodes[1].Type != "paragraph" || len(nodes[1].Content) != 1 || nodes[1].Content[0].Marks[0].Type != "link" || nodes[1].Content[0].Marks[0].Attrs["href"] != "https://example.com" {
		t.Fatalf("bookmark was not converted to a link: %#v", nodes[1])
	}
	if nodes[2].Type != "embed" || nodes[2].Attrs["src"] != "https://example.com/embed" || nodes[2].Attrs["provider"] != "iframe" {
		t.Fatalf("iframe was not converted to an embed: %#v", nodes[2])
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

func TestParsePDFImport(t *testing.T) {
	data := minimalTestPDF("Hello PDF")
	nodes, err := parsePDF(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || importPlainText(nodes[0].Content) != "Hello PDF" {
		t.Fatalf("unexpected PDF nodes: %#v", nodes)
	}
}

func TestParseScannedPDFWithoutOCRExplainsConfiguration(t *testing.T) {
	data := minimalTestPDF("")
	_, err := parsePDF(strings.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "pdf_ocr") {
		t.Fatalf("expected OCR configuration guidance, got %v", err)
	}
}

func minimalTestPDF(text string) string {
	var document strings.Builder
	document.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s) Tj ET\n", text)
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	for index, object := range objects {
		offsets = append(offsets, document.Len())
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xrefOffset := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&document, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return document.String()
}
