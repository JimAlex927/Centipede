package http

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type pageExportRequest struct {
	PageID             string `json:"pageId"`
	Format             string `json:"format"`
	IncludeChildren    bool   `json:"includeChildren"`
	IncludeAttachments bool   `json:"includeAttachments"`
}

func (handler *Handler) exportPage(c *gin.Context) {
	var request pageExportRequest
	if !decode(c, &request) || request.PageID == "" {
		return
	}
	if request.Format != "html" && request.Format != "markdown" {
		writeError(c, http.StatusBadRequest, "format must be html or markdown")
		return
	}
	if request.IncludeChildren || request.IncludeAttachments {
		writeError(c, http.StatusNotImplemented, "Child and attachment export is not implemented in Go yet")
		return
	}

	current := currentPrincipal(c)
	page, err := handler.repository.PageByID(c.Request.Context(), request.PageID, request.PageID, current.Workspace.ID, false)
	if err != nil {
		handler.writeRepositoryError(c, err, "Page not found")
		return
	}
	if !handler.requirePageAccess(c, page, false) {
		return
	}

	title := exportTitle(page.Title)
	var body string
	if request.Format == "html" {
		body = "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title></head><body><h1>" + html.EscapeString(title) + "</h1>" + renderDocumentHTML(page.Content) + "</body></html>"
	} else {
		body = "# " + markdownText(title) + "\n\n" + renderDocumentMarkdown(page.Content)
	}

	extension := "." + request.Format
	contentType := "text/markdown; charset=utf-8"
	if request.Format == "html" {
		contentType = "text/html; charset=utf-8"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(safeExportFileName(title)+extension)+`"`)
	c.String(http.StatusOK, body)
}

func exportTitle(title *string) string {
	if title == nil || strings.TrimSpace(*title) == "" {
		return "untitled"
	}
	return strings.TrimSpace(*title)
}

func safeExportFileName(value string) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		default:
			return r
		}
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "untitled"
	}
	return value
}

func renderDocumentHTML(content []byte) string {
	var document exportNode
	if err := decodeExportNode(content, &document); err != nil {
		return ""
	}
	return renderHTMLNodes(document.Content)
}

func decodeExportNode(content []byte, node *exportNode) error {
	if len(content) == 0 || string(content) == "null" {
		return nil
	}
	return jsonUnmarshal(content, node)
}

type exportNode struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs"`
	Content []exportNode   `json:"content"`
	Text    string         `json:"text"`
	Marks   []exportMark   `json:"marks"`
}

type exportMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

func renderHTMLNodes(nodes []exportNode) string {
	var result strings.Builder
	for _, node := range nodes {
		result.WriteString(renderHTMLNode(node))
	}
	return result.String()
}

func renderHTMLNode(node exportNode) string {
	if node.Type == "text" {
		value := html.EscapeString(node.Text)
		for _, mark := range node.Marks {
			switch mark.Type {
			case "bold":
				value = "<strong>" + value + "</strong>"
			case "italic":
				value = "<em>" + value + "</em>"
			case "strike":
				value = "<s>" + value + "</s>"
			case "underline":
				value = "<u>" + value + "</u>"
			case "code":
				value = "<code>" + value + "</code>"
			case "link":
				if href := exportAttrString(mark.Attrs, "href"); href != "" {
					value = `<a href="` + html.EscapeString(href) + `">` + value + "</a>"
				}
			}
		}
		return value
	}
	inner := renderHTMLNodes(node.Content)
	switch node.Type {
	case "doc":
		return inner
	case "paragraph":
		return "<p>" + inner + "</p>"
	case "heading":
		level := exportAttrInt(node.Attrs, "level", 1)
		if level < 1 || level > 6 {
			level = 1
		}
		return fmt.Sprintf("<h%d>%s</h%d>", level, inner, level)
	case "bulletList":
		return "<ul>" + inner + "</ul>"
	case "orderedList":
		return "<ol>" + inner + "</ol>"
	case "listItem":
		return "<li>" + inner + "</li>"
	case "blockquote":
		return "<blockquote>" + inner + "</blockquote>"
	case "codeBlock":
		return "<pre><code>" + html.EscapeString(exportPlainText(node.Content)) + "</code></pre>"
	case "hardBreak":
		return "<br>"
	case "horizontalRule":
		return "<hr>"
	case "image":
		src := exportAttrString(node.Attrs, "src")
		if src == "" {
			return ""
		}
		alt := exportAttrString(node.Attrs, "alt")
		return `<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(alt) + `">`
	case "table":
		return "<table>" + inner + "</table>"
	case "tableRow":
		return "<tr>" + inner + "</tr>"
	case "tableHeader":
		return "<th>" + inner + "</th>"
	case "tableCell":
		return "<td>" + inner + "</td>"
	default:
		return inner
	}
}

func renderDocumentMarkdown(content []byte) string {
	var document exportNode
	if err := decodeExportNode(content, &document); err != nil {
		return ""
	}
	return strings.TrimSpace(renderMarkdownNodes(document.Content)) + "\n"
}

func renderMarkdownNodes(nodes []exportNode) string {
	var result strings.Builder
	for _, node := range nodes {
		result.WriteString(renderMarkdownNode(node, 0))
	}
	return result.String()
}

func renderMarkdownNode(node exportNode, depth int) string {
	if node.Type == "text" {
		value := markdownText(node.Text)
		for _, mark := range node.Marks {
			switch mark.Type {
			case "bold":
				value = "**" + value + "**"
			case "italic":
				value = "*" + value + "*"
			case "strike":
				value = "~~" + value + "~~"
			case "code":
				value = "`" + value + "`"
			case "link":
				if href := exportAttrString(mark.Attrs, "href"); href != "" {
					value = "[" + value + "](" + href + ")"
				}
			}
		}
		return value
	}
	inner := renderMarkdownNodes(node.Content)
	switch node.Type {
	case "paragraph":
		return strings.TrimSpace(inner) + "\n\n"
	case "heading":
		return strings.Repeat("#", exportAttrInt(node.Attrs, "level", 1)) + " " + strings.TrimSpace(inner) + "\n\n"
	case "blockquote":
		return "> " + strings.ReplaceAll(strings.TrimSpace(inner), "\n", "\n> ") + "\n\n"
	case "bulletList":
		return renderMarkdownList(node.Content, depth, false)
	case "orderedList":
		return renderMarkdownList(node.Content, depth, true)
	case "listItem":
		return inner
	case "codeBlock":
		return "```\n" + exportPlainText(node.Content) + "\n```\n\n"
	case "hardBreak":
		return "\n"
	case "horizontalRule":
		return "---\n\n"
	case "image":
		src := exportAttrString(node.Attrs, "src")
		if src == "" {
			return ""
		}
		return "![" + markdownText(exportAttrString(node.Attrs, "alt")) + "](" + src + ")\n\n"
	case "table":
		return strings.TrimSpace(inner) + "\n\n"
	case "tableRow":
		return "| " + strings.TrimSpace(inner) + " |\n"
	case "tableHeader", "tableCell":
		return strings.TrimSpace(inner) + " | "
	default:
		return inner
	}
}

func renderMarkdownList(nodes []exportNode, depth int, ordered bool) string {
	var result strings.Builder
	indent := strings.Repeat("  ", depth)
	for index, node := range nodes {
		prefix := "- "
		if ordered {
			prefix = strconv.Itoa(index+1) + ". "
		}
		result.WriteString(indent + prefix + strings.TrimSpace(renderMarkdownNodes(node.Content)) + "\n")
	}
	return result.String() + "\n"
}

func exportPlainText(nodes []exportNode) string {
	var result strings.Builder
	for _, node := range nodes {
		if node.Type == "text" {
			result.WriteString(node.Text)
		} else {
			result.WriteString(exportPlainText(node.Content))
		}
	}
	return result.String()
}

func exportAttrString(attrs map[string]any, key string) string {
	if value, ok := attrs[key].(string); ok {
		return value
	}
	return ""
}

func exportAttrInt(attrs map[string]any, key string, fallback int) int {
	value, ok := attrs[key]
	if !ok {
		return fallback
	}
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	default:
		return fallback
	}
}

func markdownText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\n", "  \n")
}

var jsonUnmarshal = json.Unmarshal
