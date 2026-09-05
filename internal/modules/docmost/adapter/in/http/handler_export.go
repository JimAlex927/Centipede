package http

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"

	"centipede/internal/modules/docmost/domain"

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
	if request.IncludeAttachments {
		writeError(c, http.StatusNotImplemented, "Attachment export is not implemented in Go yet")
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
	pages, err := handler.repository.ExportPageTree(c.Request.Context(), page.ID, current.Workspace.ID, current.User.ID, isAdmin(current.User), request.IncludeChildren)
	if err != nil || len(pages) == 0 {
		writeError(c, http.StatusInternalServerError, "Failed to load pages for export")
		return
	}
	if request.IncludeChildren {
		archive, archiveErr := buildPagesZip(pages, request.Format, page.ID)
		if archiveErr != nil {
			writeError(c, http.StatusInternalServerError, "Failed to create export archive")
			return
		}
		c.Header("Content-Type", "application/zip")
		c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(safeExportFileName(exportTitle(page.Title))+`.zip"`))
		c.Data(http.StatusOK, "application/zip", archive)
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

func (handler *Handler) exportSpace(c *gin.Context) {
	var request struct {
		SpaceID            string `json:"spaceId"`
		Format             string `json:"format"`
		IncludeAttachments bool   `json:"includeAttachments"`
	}
	if !decode(c, &request) {
		return
	}
	if request.SpaceID == "" || (request.Format != "html" && request.Format != "markdown") {
		writeError(c, http.StatusBadRequest, "spaceId and a valid format are required")
		return
	}
	if request.IncludeAttachments {
		writeError(c, http.StatusNotImplemented, "Attachment export is not implemented in Go yet")
		return
	}
	current := currentPrincipal(c)
	if !handler.requireSpaceRole(c, request.SpaceID, "admin") {
		return
	}
	space, err := handler.repository.SpaceByID(c.Request.Context(), request.SpaceID, current.Workspace.ID, current.User.ID)
	if err != nil {
		handler.writeRepositoryError(c, err, "Space not found")
		return
	}
	pages, err := handler.repository.ExportSpacePages(c.Request.Context(), space.ID, current.Workspace.ID, current.User.ID, isAdmin(current.User))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to load space pages for export")
		return
	}
	archive, err := buildPagesZip(pages, request.Format, "")
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create export archive")
		return
	}
	name := exportTitle(space.Name) + "-space-export.zip"
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(safeExportFileName(name))+`"`)
	c.Data(http.StatusOK, "application/zip", archive)
}

func buildPagesZip(pages []domain.Page, format, rootID string) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	paths := make(map[string]string, len(pages))
	used := make(map[string]int)
	extension := "." + format
	pageByID := make(map[string]domain.Page, len(pages))
	for _, page := range pages {
		pageByID[page.ID] = page
	}
	depthMemo := make(map[string]int, len(pages))
	orderedPages := append([]domain.Page(nil), pages...)
	sort.SliceStable(orderedPages, func(i, j int) bool {
		return pageExportDepth(orderedPages[i].ID, pageByID, rootID, depthMemo, map[string]bool{}) <
			pageExportDepth(orderedPages[j].ID, pageByID, rootID, depthMemo, map[string]bool{})
	})
	for _, page := range orderedPages {
		base := safeExportFileName(exportTitle(page.Title))
		parentPath := ""
		if page.ParentPageID != nil && page.ID != rootID {
			parentPath = paths[*page.ParentPageID]
		}
		if page.ID == rootID {
			parentPath = ""
		}
		key := parentPath + "\x00" + base
		used[key]++
		if used[key] > 1 {
			base += " (" + strconv.Itoa(used[key]) + ")"
		}
		filePath := pathpkg.Join(parentPath, base+extension)
		paths[page.ID] = strings.TrimSuffix(filePath, extension)
		content := ""
		if format == "html" {
			content = pageHTMLDocument(page)
		} else {
			content = "# " + markdownText(exportTitle(page.Title)) + "\n\n" + renderDocumentMarkdown(page.Content)
		}
		entry, err := archive.Create(filePath)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err = entry.Write([]byte(content)); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func pageExportDepth(pageID string, pages map[string]domain.Page, rootID string, memo map[string]int, visiting map[string]bool) int {
	if pageID == rootID {
		return 0
	}
	if depth, ok := memo[pageID]; ok {
		return depth
	}
	if visiting[pageID] {
		return 0
	}
	page, ok := pages[pageID]
	if !ok || page.ParentPageID == nil {
		memo[pageID] = 0
		return 0
	}
	visiting[pageID] = true
	depth := pageExportDepth(*page.ParentPageID, pages, rootID, memo, visiting) + 1
	delete(visiting, pageID)
	memo[pageID] = depth
	return depth
}

func pageHTMLDocument(page domain.Page) string {
	title := exportTitle(page.Title)
	return "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title></head><body><h1>" + html.EscapeString(title) + "</h1>" + renderDocumentHTML(page.Content) + "</body></html>"
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
