package http

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
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
	var attachments []zipExportAttachment
	if request.IncludeAttachments {
		attachments, err = handler.loadExportAttachments(c.Request.Context(), pages)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to load attachments for export")
			return
		}
	}
	if request.IncludeChildren || request.IncludeAttachments {
		archive, archiveErr := buildPagesZipWithAttachments(pages, request.Format, page.ID, attachments)
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
	attachments := []zipExportAttachment(nil)
	if request.IncludeAttachments {
		attachments, err = handler.loadExportAttachments(c.Request.Context(), pages)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "Failed to load attachments for export")
			return
		}
	}
	archive, err := buildPagesZipWithAttachments(pages, request.Format, "", attachments)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create export archive")
		return
	}
	name := exportTitle(space.Name) + "-space-export.zip"
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(safeExportFileName(name))+`"`)
	c.Data(http.StatusOK, "application/zip", archive)
}

type zipExportAttachment struct {
	Attachment domain.Attachment
	Data       []byte
}

func buildPagesZip(pages []domain.Page, format, rootID string) ([]byte, error) {
	return buildPagesZipWithAttachments(pages, format, rootID, nil)
}

func buildPagesZipWithAttachments(pages []domain.Page, format, rootID string, attachments []zipExportAttachment) ([]byte, error) {
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
		pageContent := page.Content
		if len(attachments) > 0 {
			pageContent = rewriteExportAttachmentLinks(page.Content, attachments, pathpkg.Dir(filePath))
		}
		if format == "html" {
			content = pageHTMLDocumentWithContent(page, pageContent)
		} else {
			content = "# " + markdownText(exportTitle(page.Title)) + "\n\n" + renderDocumentMarkdown(pageContent)
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
	for _, attachment := range attachments {
		entryPath := pathpkg.Join("files", attachment.Attachment.ID, safeExportFileName(attachment.Attachment.FileName))
		entry, err := archive.Create(entryPath)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err = entry.Write(attachment.Data); err != nil {
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
	return pageHTMLDocumentWithContent(page, page.Content)
}

func pageHTMLDocumentWithContent(page domain.Page, content []byte) string {
	title := exportTitle(page.Title)
	return "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>" + html.EscapeString(title) + "</title></head><body><h1>" + html.EscapeString(title) + "</h1>" + renderDocumentHTML(content) + "</body></html>"
}

func (handler *Handler) loadExportAttachments(ctx context.Context, pages []domain.Page) ([]zipExportAttachment, error) {
	if len(pages) == 0 {
		return []zipExportAttachment{}, nil
	}
	pageIDs := make(map[string]bool, len(pages))
	attachmentIDs := make(map[string]bool)
	for _, page := range pages {
		pageIDs[page.ID] = true
		for _, attachmentID := range exportAttachmentIDs(page.Content) {
			attachmentIDs[attachmentID] = true
		}
	}
	ids := make([]string, 0, len(attachmentIDs))
	for attachmentID := range attachmentIDs {
		ids = append(ids, attachmentID)
	}
	items, err := handler.repository.AttachmentsByIDs(ctx, ids, pages[0].WorkspaceID)
	if err != nil {
		return nil, err
	}
	result := make([]zipExportAttachment, 0, len(items))
	for _, item := range items {
		if item.PageID == nil || !pageIDs[*item.PageID] || handler.storage == nil {
			continue
		}
		file, openErr := handler.storage.Open(ctx, item.FilePath)
		if openErr != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 100*1024*1024+1))
		_ = file.Close()
		if readErr != nil || len(data) > 100*1024*1024 {
			continue
		}
		result = append(result, zipExportAttachment{Attachment: item, Data: data})
	}
	return result, nil
}

func exportAttachmentIDs(content []byte) []string {
	var value any
	if len(content) == 0 || json.Unmarshal(content, &value) != nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	var walk func(any)
	walk = func(current any) {
		switch node := current.(type) {
		case []any:
			for _, child := range node {
				walk(child)
			}
		case map[string]any:
			if attrs, ok := node["attrs"].(map[string]any); ok {
				if attachmentID, ok := attrs["attachmentId"].(string); ok && attachmentID != "" && !seen[attachmentID] {
					seen[attachmentID] = true
					result = append(result, attachmentID)
				}
			}
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(value)
	return result
}

func rewriteExportAttachmentLinks(content []byte, attachments []zipExportAttachment, pageDir string) []byte {
	var value any
	if len(content) == 0 || json.Unmarshal(content, &value) != nil {
		return content
	}
	paths := make(map[string]string, len(attachments))
	for _, attachment := range attachments {
		entryPath := pathpkg.Join("files", attachment.Attachment.ID, safeExportFileName(attachment.Attachment.FileName))
		paths[attachment.Attachment.ID] = exportRelativePath(pageDir, entryPath)
	}
	var walk func(any)
	walk = func(current any) {
		object, ok := current.(map[string]any)
		if !ok {
			if list, listOK := current.([]any); listOK {
				for _, child := range list {
					walk(child)
				}
			}
			return
		}
		if attrs, ok := object["attrs"].(map[string]any); ok {
			if attachmentID, ok := attrs["attachmentId"].(string); ok {
				if relative, found := paths[attachmentID]; found {
					attrs["src"] = relative
				}
			}
		}
		for _, child := range object {
			walk(child)
		}
	}
	walk(value)
	result, err := json.Marshal(value)
	if err != nil {
		return content
	}
	return result
}

func exportRelativePath(from, to string) string {
	from = pathpkg.Clean(from)
	if from == "." || from == "" {
		return to
	}
	depth := len(strings.Split(strings.Trim(from, "/"), "/"))
	return pathpkg.Join(strings.Repeat("../", depth), to)
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
