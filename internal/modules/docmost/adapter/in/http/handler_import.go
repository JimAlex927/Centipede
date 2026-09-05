package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	htmlnode "golang.org/x/net/html"

	"centipede/internal/modules/docmost/adapter/out/postgres"

	"github.com/gin-gonic/gin"
)

const singlePageImportLimit = 30 * 1024 * 1024

type importNode struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []importNode   `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []importMark   `json:"marks,omitempty"`
}

type importMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

func (handler *Handler) importPage(c *gin.Context) {
	current := currentPrincipal(c)
	spaceID := strings.TrimSpace(c.PostForm("spaceId"))
	if spaceID == "" {
		writeError(c, http.StatusBadRequest, "spaceId is required")
		return
	}
	if !handler.requireSpaceRole(c, spaceID, "writer") {
		return
	}
	file, err := c.FormFile("file")
	if err != nil || file == nil {
		writeError(c, http.StatusBadRequest, "Failed to upload file")
		return
	}
	if file.Size > singlePageImportLimit {
		writeError(c, http.StatusBadRequest, "File too large. Exceeds the 30mb import limit")
		return
	}
	extension := strings.ToLower(filepath.Ext(file.Filename))
	if extension != ".md" && extension != ".html" {
		if extension == ".docx" || extension == ".pdf" {
			writeError(c, http.StatusNotImplemented, "DOCX and PDF import is not implemented in Go yet")
		} else {
			writeError(c, http.StatusBadRequest, "Invalid import file type.")
		}
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read uploaded file")
		return
	}
	defer opened.Close()
	nodes, err := parseImportedDocument(opened, extension)
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to parse imported file")
		return
	}
	title, nodes := extractImportedTitle(nodes, strings.TrimSuffix(filepath.Base(file.Filename), extension))
	content, err := json.Marshal(importNode{Type: "doc", Content: nodes})
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to encode imported page")
		return
	}
	page, err := handler.repository.CreatePage(c.Request.Context(), current.Workspace.ID, current.User.ID, postgres.PageInput{
		Title: &title, SpaceID: &spaceID, Content: content,
	})
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to create imported page")
		return
	}
	writeData(c, http.StatusOK, page)
}

func parseImportedDocument(reader io.Reader, extension string) ([]importNode, error) {
	if extension == ".md" {
		return parseMarkdown(reader)
	}
	document, err := htmlnode.Parse(reader)
	if err != nil {
		return nil, err
	}
	return parseHTMLRoot(document), nil
}

func parseMarkdown(reader io.Reader) ([]importNode, error) {
	data, err := io.ReadAll(io.LimitReader(reader, singlePageImportLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > singlePageImportLimit {
		return nil, errors.New("import file is too large")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	nodes := make([]importNode, 0)
	for index := 0; index < len(lines); {
		line := strings.TrimSpace(lines[index])
		if line == "" {
			index++
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence := line[:3]
			index++
			var code strings.Builder
			for index < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[index]), fence) {
				if code.Len() > 0 {
					code.WriteByte('\n')
				}
				code.WriteString(lines[index])
				index++
			}
			if index < len(lines) {
				index++
			}
			nodes = append(nodes, importNode{Type: "codeBlock", Content: []importNode{{Type: "text", Text: code.String()}}})
			continue
		}
		if level, text, ok := markdownHeading(line); ok {
			nodes = append(nodes, importNode{Type: "heading", Attrs: map[string]any{"level": level}, Content: markdownInline(text)})
			index++
			continue
		}
		if isMarkdownRule(line) {
			nodes = append(nodes, importNode{Type: "horizontalRule"})
			index++
			continue
		}
		if _, ordered, _, ok := markdownListItem(line); ok {
			items := make([]importNode, 0)
			listType := "bulletList"
			if ordered {
				listType = "orderedList"
			}
			for index < len(lines) {
				current := strings.TrimSpace(lines[index])
				currentMarker, currentOrdered, currentText, currentOK := markdownListItem(current)
				if !currentOK || currentOrdered != ordered || currentMarker == "" {
					break
				}
				items = append(items, importNode{Type: "listItem", Content: []importNode{{Type: "paragraph", Content: markdownInline(currentText)}}})
				index++
			}
			nodes = append(nodes, importNode{Type: listType, Content: items})
			continue
		}
		var paragraph strings.Builder
		for index < len(lines) {
			current := strings.TrimSpace(lines[index])
			if current == "" || strings.HasPrefix(current, "```") || strings.HasPrefix(current, "~~~") {
				break
			}
			if _, _, heading := markdownHeading(current); heading {
				break
			}
			if paragraph.Len() > 0 {
				paragraph.WriteByte('\n')
			}
			paragraph.WriteString(current)
			index++
		}
		nodes = append(nodes, importNode{Type: "paragraph", Content: markdownInline(paragraph.String())})
	}
	return ensureImportContent(nodes), nil
}

func markdownHeading(line string) (int, string, bool) {
	count := 0
	for count < len(line) && line[count] == '#' {
		count++
	}
	if count == 0 || count > 6 || count >= len(line) || line[count] != ' ' {
		return 0, "", false
	}
	return count, strings.TrimSpace(line[count+1:]), true
}

func isMarkdownRule(line string) bool {
	return regexp.MustCompile(`^(\*\s*){3,}$`).MatchString(line) || regexp.MustCompile(`^(-\s*){3,}$`).MatchString(line) || regexp.MustCompile(`^(_\s*){3,}$`).MatchString(line)
}

func markdownListItem(line string) (string, bool, string, bool) {
	for _, marker := range []string{"-", "*", "+"} {
		prefix := marker + " "
		if strings.HasPrefix(line, prefix) {
			return marker, false, strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	for index := 0; index < len(line); index++ {
		if line[index] == '.' && index > 0 && index+1 < len(line) && line[index+1] == ' ' {
			if _, err := strconv.Atoi(line[:index]); err == nil {
				return line[:index], true, strings.TrimSpace(line[index+2:]), true
			}
		}
	}
	return "", false, "", false
}

func markdownInline(value string) []importNode {
	result := make([]importNode, 0)
	tokens := []struct{ token, kind string }{{"**", "bold"}, {"__", "bold"}, {"*", "italic"}, {"_", "italic"}, {"`", "code"}}
	for len(value) > 0 {
		start := len(value)
		selected := struct{ token, kind string }{}
		for _, candidate := range tokens {
			if index := strings.Index(value, candidate.token); index >= 0 && index < start {
				start, selected = index, candidate
			}
		}
		if start == len(value) {
			result = append(result, importNode{Type: "text", Text: value})
			break
		}
		if start > 0 {
			result = append(result, importNode{Type: "text", Text: value[:start]})
			value = value[start:]
		}
		width := len(selected.token)
		end := strings.Index(value[width:], selected.token)
		if end < 0 {
			result = append(result, importNode{Type: "text", Text: value})
			break
		}
		result = append(result, importNode{Type: "text", Text: value[width : width+end], Marks: []importMark{{Type: selected.kind}}})
		value = value[width+end+width:]
	}
	return result
}

func parseHTMLRoot(root *htmlnode.Node) []importNode {
	var result []importNode
	var walk func(*htmlnode.Node)
	walk = func(node *htmlnode.Node) {
		if node.Type == htmlnode.ElementNode {
			tag := strings.ToLower(node.Data)
			switch {
			case tag == "html" || tag == "head" || tag == "body" || tag == "main":
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					walk(child)
				}
				return
			case tag >= "h1" && tag <= "h6":
				level, _ := strconv.Atoi(tag[1:])
				result = append(result, importNode{Type: "heading", Attrs: map[string]any{"level": level}, Content: htmlInline(node)})
				return
			case tag == "p":
				result = append(result, importNode{Type: "paragraph", Content: htmlInline(node)})
				return
			case tag == "ul" || tag == "ol":
				list := importNode{Type: "bulletList", Content: []importNode{}}
				if tag == "ol" {
					list.Type = "orderedList"
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == htmlnode.ElementNode && strings.EqualFold(child.Data, "li") {
						list.Content = append(list.Content, importNode{Type: "listItem", Content: []importNode{{Type: "paragraph", Content: htmlInline(child)}}})
					}
				}
				result = append(result, list)
				return
			case tag == "blockquote":
				result = append(result, importNode{Type: "blockquote", Content: htmlBlockChildren(node)})
				return
			case tag == "pre":
				result = append(result, importNode{Type: "codeBlock", Content: []importNode{{Type: "text", Text: htmlText(node)}}})
				return
			case tag == "hr":
				result = append(result, importNode{Type: "horizontalRule"})
				return
			case tag == "table":
				result = append(result, importNode{Type: "table", Content: htmlBlockChildren(node)})
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return ensureImportContent(result)
}

func htmlBlockChildren(node *htmlnode.Node) []importNode {
	result := make([]importNode, 0)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != htmlnode.ElementNode {
			continue
		}
		tag := strings.ToLower(child.Data)
		switch tag {
		case "p":
			result = append(result, importNode{Type: "paragraph", Content: htmlInline(child)})
		case "tr":
			result = append(result, importNode{Type: "tableRow", Content: htmlBlockChildren(child)})
		case "th":
			result = append(result, importNode{Type: "tableHeader", Content: htmlInline(child)})
		case "td":
			result = append(result, importNode{Type: "tableCell", Content: htmlInline(child)})
		default:
			result = append(result, htmlBlockChildren(child)...)
		}
	}
	return result
}

func htmlInline(node *htmlnode.Node) []importNode {
	result := make([]importNode, 0)
	var walk func(*htmlnode.Node, []importMark)
	walk = func(current *htmlnode.Node, marks []importMark) {
		if current.Type == htmlnode.TextNode {
			if strings.TrimSpace(current.Data) != "" {
				result = append(result, importNode{Type: "text", Text: current.Data, Marks: marks})
			}
			return
		}
		if current.Type == htmlnode.ElementNode {
			tag := strings.ToLower(current.Data)
			if tag == "br" {
				result = append(result, importNode{Type: "hardBreak"})
				return
			}
			if tag == "img" {
				attrs := map[string]any{}
				for _, attr := range current.Attr {
					if attr.Key == "src" || attr.Key == "alt" {
						attrs[attr.Key] = attr.Val
					}
				}
				result = append(result, importNode{Type: "image", Attrs: attrs})
				return
			}
			next := marks
			if tag == "strong" || tag == "b" {
				next = appendMarks(marks, "bold")
			}
			if tag == "em" || tag == "i" {
				next = appendMarks(marks, "italic")
			}
			if tag == "code" {
				next = appendMarks(marks, "code")
			}
			if tag == "a" {
				for _, attr := range current.Attr {
					if attr.Key == "href" {
						next = append(next, importMark{Type: "link", Attrs: map[string]any{"href": attr.Val}})
					}
				}
			}
			for child := current.FirstChild; child != nil; child = child.NextSibling {
				walk(child, next)
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walk(child, nil)
	}
	return result
}

func appendMarks(marks []importMark, kind string) []importMark {
	result := append([]importMark{}, marks...)
	return append(result, importMark{Type: kind})
}

func htmlText(node *htmlnode.Node) string {
	var result strings.Builder
	var walk func(*htmlnode.Node)
	walk = func(current *htmlnode.Node) {
		if current.Type == htmlnode.TextNode {
			result.WriteString(current.Data)
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return result.String()
}

func extractImportedTitle(nodes []importNode, fallback string) (string, []importNode) {
	title := strings.TrimSpace(fallback)
	if title == "" {
		title = "Untitled"
	}
	if len(nodes) > 0 && nodes[0].Type == "heading" {
		candidate := strings.TrimSpace(importPlainText(nodes[0].Content))
		if candidate != "" {
			title = candidate
			nodes = nodes[1:]
		}
	}
	return title, ensureImportContent(nodes)
}

func importPlainText(nodes []importNode) string {
	var result strings.Builder
	for _, node := range nodes {
		if node.Type == "text" {
			result.WriteString(node.Text)
		} else {
			result.WriteString(importPlainText(node.Content))
		}
	}
	return result.String()
}

func ensureImportContent(nodes []importNode) []importNode {
	if len(nodes) == 0 {
		return []importNode{{Type: "paragraph"}}
	}
	return nodes
}
