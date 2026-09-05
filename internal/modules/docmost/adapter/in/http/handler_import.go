package http

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	htmlnode "golang.org/x/net/html"

	"centipede/internal/modules/docmost/adapter/out/postgres"
	"centipede/internal/platform/config"

	"github.com/gin-gonic/gin"
	"github.com/ledongthuc/pdf"
)

const singlePageImportLimit = 30 * 1024 * 1024

var errPDFOCRNotConfigured = errors.New("PDF contains no extractable text; configure pdf_ocr for scanned PDFs")

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
	if extension != ".md" && extension != ".html" && extension != ".docx" && extension != ".pdf" {
		writeError(c, http.StatusBadRequest, "Invalid import file type.")
		return
	}
	if extension == ".docx" && !handler.requireFeature(c, "import:docx") {
		return
	}
	if extension == ".pdf" && !handler.requireFeature(c, "import:pdf") {
		return
	}
	opened, err := file.Open()
	if err != nil {
		writeError(c, http.StatusBadRequest, "Failed to read uploaded file")
		return
	}
	defer opened.Close()
	nodes, err := parseImportedDocumentWithOCR(opened, extension, handler.pdfOCR)
	if err != nil {
		if errors.Is(err, errPDFOCRNotConfigured) {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}
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
	return parseImportedDocumentWithOCR(reader, extension, config.PDFOCRConfig{})
}

func parseImportedDocumentWithOCR(reader io.Reader, extension string, ocrConfig config.PDFOCRConfig) ([]importNode, error) {
	if extension == ".md" {
		return parseMarkdown(reader)
	}
	if extension == ".docx" {
		return parseDocx(reader)
	}
	if extension == ".csv" {
		return parseCSV(reader)
	}
	if extension == ".pdf" {
		return parsePDFWithOCR(reader, ocrConfig)
	}
	document, err := htmlnode.Parse(reader)
	if err != nil {
		return nil, err
	}
	normalizeImportedHTML(document)
	return parseHTMLRoot(document), nil
}

func parseCSV(reader io.Reader) ([]importNode, error) {
	parser := csv.NewReader(io.LimitReader(reader, singlePageImportLimit+1))
	parser.FieldsPerRecord = -1
	parser.ReuseRecord = false
	rows := make([][]string, 0, 64)
	for len(rows) < 10000 {
		row, err := parser.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(row) > 1000 {
			return nil, errors.New("CSV contains too many columns")
		}
		for _, value := range row {
			if len(value) > 1<<20 {
				return nil, errors.New("CSV field is too large")
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 10000 {
		return nil, errors.New("CSV contains too many rows")
	}
	if len(rows) == 0 {
		return ensureImportContent(nil), nil
	}
	result := importNode{Type: "table", Content: make([]importNode, 0, len(rows))}
	for rowIndex, row := range rows {
		cells := make([]importNode, 0, len(row))
		for _, value := range row {
			cellType := "tableCell"
			if rowIndex == 0 {
				cellType = "tableHeader"
			}
			cells = append(cells, importNode{Type: cellType, Content: []importNode{{Type: "paragraph", Content: markdownInline(value)}}})
		}
		result.Content = append(result.Content, importNode{Type: "tableRow", Content: cells})
	}
	return []importNode{result}, nil
}

func parsePDF(reader io.Reader) ([]importNode, error) {
	return parsePDFWithOCR(reader, config.PDFOCRConfig{})
}

func parsePDFWithOCR(reader io.Reader, ocrConfig config.PDFOCRConfig) ([]importNode, error) {
	data, err := io.ReadAll(io.LimitReader(reader, singlePageImportLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > singlePageImportLimit {
		return nil, errors.New("import file is too large")
	}
	temporary, err := os.CreateTemp("", "docmost-import-*.pdf")
	if err != nil {
		return nil, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err = temporary.Close(); err != nil {
		return nil, err
	}
	value, extractionErr := extractPDFText(temporaryName)
	if value == "" {
		if ocrConfig.TesseractPath == "" || ocrConfig.PDFToPNGPath == "" {
			if extractionErr != nil {
				return nil, extractionErr
			}
			return nil, errPDFOCRNotConfigured
		}
		value, err = extractPDFTextWithOCR(temporaryName, ocrConfig)
		if err != nil {
			return nil, err
		}
	}
	if value == "" {
		return nil, errors.New("PDF OCR produced no text")
	}
	return []importNode{{Type: "paragraph", Content: []importNode{{Type: "text", Text: value}}}}, nil
}

func extractPDFText(fileName string) (string, error) {
	file, document, err := pdf.Open(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()
	plainText, err := document.GetPlainText()
	if err != nil {
		return "", err
	}
	text, err := io.ReadAll(io.LimitReader(plainText, singlePageImportLimit+1))
	if err != nil {
		return "", err
	}
	if len(text) > singlePageImportLimit {
		return "", errors.New("import file is too large")
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(string(text), "\r\n", "\n"), "\r", "\n")), nil
}

func extractPDFTextWithOCR(fileName string, ocrConfig config.PDFOCRConfig) (string, error) {
	if ocrConfig.Timeout <= 0 {
		ocrConfig.Timeout = 2 * time.Minute
	}
	if ocrConfig.MaxPages <= 0 {
		ocrConfig.MaxPages = 32
	}
	if strings.TrimSpace(ocrConfig.Language) == "" {
		ocrConfig.Language = "eng"
	}
	workDir, err := os.MkdirTemp("", "docmost-pdf-ocr-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)
	prefix := filepath.Join(workDir, "page")
	ctx, cancel := context.WithTimeout(context.Background(), ocrConfig.Timeout)
	defer cancel()
	render := exec.CommandContext(ctx, ocrConfig.PDFToPNGPath, "-png", "-r", "200", "-f", "1", "-l", strconv.Itoa(ocrConfig.MaxPages), fileName, prefix)
	if output, err := render.CombinedOutput(); err != nil {
		return "", fmt.Errorf("render PDF for OCR: %w: %s", err, strings.TrimSpace(string(output)))
	}
	images, err := filepath.Glob(prefix + "-*.png")
	if err != nil {
		return "", err
	}
	sort.Strings(images)
	if len(images) == 0 {
		return "", errors.New("PDF OCR renderer produced no page images")
	}
	var text strings.Builder
	for _, image := range images {
		ocr := exec.CommandContext(ctx, ocrConfig.TesseractPath, image, "stdout", "-l", ocrConfig.Language)
		output, err := ocr.Output()
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("OCR timed out: %w", ctx.Err())
			}
			return "", fmt.Errorf("run tesseract: %w", err)
		}
		pageText := strings.TrimSpace(string(output))
		if pageText == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(pageText)
		if text.Len() > singlePageImportLimit {
			return "", errors.New("OCR result is too large")
		}
	}
	return strings.TrimSpace(text.String()), nil
}

type docxImportParagraph struct {
	node    importNode
	isList  bool
	ordered bool
	numID   string
}

func parseDocx(reader io.Reader) ([]importNode, error) {
	data, err := io.ReadAll(io.LimitReader(reader, singlePageImportLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > singlePageImportLimit {
		return nil, errors.New("import file is too large")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var documentFile *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			documentFile = file
			break
		}
	}
	if documentFile == nil {
		return nil, errors.New("word/document.xml is missing")
	}
	orderedNumbering, err := readDocxNumbering(archive)
	if err != nil {
		return nil, err
	}
	opened, err := documentFile.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()

	decoder := xml.NewDecoder(io.LimitReader(opened, singlePageImportLimit+1))
	paragraphs := make([]docxImportParagraph, 0)
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if ok && start.Name.Local == "p" {
			paragraph, parseErr := parseDocxParagraph(decoder, start)
			if parseErr != nil {
				return nil, parseErr
			}
			paragraphs = append(paragraphs, paragraph)
		}
	}

	result := make([]importNode, 0, len(paragraphs))
	for index := range paragraphs {
		paragraphs[index].ordered = paragraphs[index].isList && orderedNumbering[paragraphs[index].numID]
	}
	for index := 0; index < len(paragraphs); {
		paragraph := paragraphs[index]
		if !paragraph.isList {
			result = append(result, paragraph.node)
			index++
			continue
		}
		list := importNode{Type: "bulletList", Content: make([]importNode, 0)}
		if paragraph.ordered {
			list.Type = "orderedList"
		}
		for index < len(paragraphs) && paragraphs[index].isList && paragraphs[index].ordered == paragraph.ordered {
			list.Content = append(list.Content, importNode{Type: "listItem", Content: []importNode{paragraphs[index].node}})
			index++
		}
		result = append(result, list)
	}
	return ensureImportContent(result), nil
}

func parseDocxParagraph(decoder *xml.Decoder, start xml.StartElement) (docxImportParagraph, error) {
	paragraph := docxImportParagraph{node: importNode{Type: "paragraph"}}
	var marks []importMark
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return paragraph, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			switch value.Name.Local {
			case "pStyle":
				style := docxAttribute(value, "val")
				if level := docxHeadingLevel(style); level > 0 {
					paragraph.node.Type = "heading"
					paragraph.node.Attrs = map[string]any{"level": level}
				} else if style == "Title" {
					paragraph.node.Type = "heading"
					paragraph.node.Attrs = map[string]any{"level": 1}
				}
			case "numPr":
				paragraph.isList = true
			case "numId":
				paragraph.numID = docxAttribute(value, "val")
			case "t":
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return paragraph, err
				}
				paragraph.node.Content = append(paragraph.node.Content, importNode{Type: "text", Text: text, Marks: append([]importMark(nil), marks...)})
				depth--
			case "b":
				marks = append(marks, importMark{Type: "bold"})
			case "i":
				marks = append(marks, importMark{Type: "italic"})
			case "strike":
				marks = append(marks, importMark{Type: "strike"})
			case "u":
				marks = append(marks, importMark{Type: "underline"})
			case "br":
				paragraph.node.Content = append(paragraph.node.Content, importNode{Type: "hardBreak"})
			case "ilvl":
				// Keep the current flat list shape; nested list indentation is
				// intentionally normalized during import.
			}
		case xml.EndElement:
			depth--
			if value.Name.Local == "r" {
				marks = nil
			}
		}
	}
	paragraph.node.Content = ensureImportInlineContent(paragraph.node.Content)
	return paragraph, nil
}

func readDocxNumbering(archive *zip.Reader) (map[string]bool, error) {
	result := make(map[string]bool)
	var numberingFile *zip.File
	for _, file := range archive.File {
		if file.Name == "word/numbering.xml" {
			numberingFile = file
			break
		}
	}
	if numberingFile == nil {
		return result, nil
	}
	opened, err := numberingFile.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	decoder := xml.NewDecoder(io.LimitReader(opened, singlePageImportLimit+1))
	abstractDecimal := make(map[string]bool)
	currentAbstract := ""
	currentNum := ""
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "abstractNum":
				currentAbstract = docxAttribute(value, "abstractNumId")
			case "num":
				currentNum = docxAttribute(value, "numId")
			case "numFmt":
				if currentAbstract != "" {
					abstractDecimal[currentAbstract] = docxAttribute(value, "val") == "decimal"
				}
			case "abstractNumId":
				if currentNum != "" {
					abstractID := docxAttribute(value, "val")
					result[currentNum] = abstractDecimal[abstractID]
				}
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "abstractNum":
				currentAbstract = ""
			case "num":
				currentNum = ""
			}
		}
	}
	return result, nil
}

func docxHeadingLevel(style string) int {
	if !strings.HasPrefix(style, "Heading") {
		return 0
	}
	level, err := strconv.Atoi(strings.TrimPrefix(style, "Heading"))
	if err != nil || level < 1 || level > 6 {
		return 0
	}
	return level
}

func docxAttribute(element xml.StartElement, key string) string {
	for _, attr := range element.Attr {
		if attr.Name.Local == key {
			return attr.Value
		}
	}
	return ""
}

func ensureImportInlineContent(nodes []importNode) []importNode {
	if len(nodes) == 0 {
		return []importNode{{Type: "text", Text: ""}}
	}
	return nodes
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
		if strings.HasPrefix(value, "![") {
			if closeAlt := strings.Index(value[2:], "]("); closeAlt >= 0 {
				closeAlt += 2
				if closeURL := strings.Index(value[closeAlt+2:], ")"); closeURL >= 0 {
					closeURL += closeAlt + 2
					result = append(result, importNode{Type: "image", Attrs: map[string]any{"alt": value[2:closeAlt], "src": strings.TrimSpace(value[closeAlt+2 : closeURL])}})
					value = value[closeURL+1:]
					continue
				}
			}
		}
		if strings.HasPrefix(value, "[") {
			if closeLabel := strings.Index(value[1:], "]("); closeLabel >= 0 {
				closeLabel++
				if closeURL := strings.Index(value[closeLabel+2:], ")"); closeURL >= 0 {
					closeURL += closeLabel + 2
					result = append(result, importNode{
						Type: "text", Text: value[1:closeLabel],
						Marks: []importMark{{Type: "link", Attrs: map[string]any{"href": strings.TrimSpace(value[closeLabel+2 : closeURL])}}},
					})
					value = value[closeURL+1:]
					continue
				}
			}
		}
		start := len(value)
		selected := struct{ token, kind string }{}
		for _, candidate := range tokens {
			if index := strings.Index(value, candidate.token); index >= 0 && index < start {
				start, selected = index, candidate
			}
		}
		if index := strings.Index(value, "!["); index >= 0 && index < start {
			start = index
		}
		if index := strings.Index(value, "["); index >= 0 && index < start {
			start = index
		}
		if start == len(value) {
			result = append(result, importNode{Type: "text", Text: value})
			break
		}
		if start > 0 {
			result = append(result, importNode{Type: "text", Text: value[:start]})
			value = value[start:]
			if selected.token == "" {
				continue
			}
		}
		if selected.token == "" {
			result = append(result, importNode{Type: "text", Text: value})
			break
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
			if special, ok := htmlSpecialBlock(node); ok {
				result = append(result, special)
				return
			}
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
				} else if htmlAttribute(node, "data-type") == "taskList" {
					list.Type = "taskList"
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == htmlnode.ElementNode && strings.EqualFold(child.Data, "li") {
						itemType := "listItem"
						if list.Type == "taskList" {
							itemType = "taskItem"
						}
						attrs := map[string]any{}
						if checked := htmlAttribute(child, "data-checked"); checked != "" {
							attrs["checked"] = checked == "true"
						}
						list.Content = append(list.Content, importNode{Type: itemType, Attrs: attrs, Content: []importNode{{Type: "paragraph", Content: htmlInline(child)}}})
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
		if special, ok := htmlSpecialBlock(child); ok {
			result = append(result, special)
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
			if special, ok := htmlSpecialBlock(current); ok {
				result = append(result, special)
				return
			}
			tag := strings.ToLower(current.Data)
			if tag == "br" {
				result = append(result, importNode{Type: "hardBreak"})
				return
			}
			if tag == "img" {
				attrs := htmlMediaAttributes(current, "src")
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

// htmlSpecialBlock preserves the custom Tiptap nodes emitted by Docmost's
// HTML exporter. A generic HTML walker would otherwise recurse into these
// atom nodes and silently discard their attachment URL and metadata.
func htmlSpecialBlock(node *htmlnode.Node) (importNode, bool) {
	if node == nil || node.Type != htmlnode.ElementNode {
		return importNode{}, false
	}
	tag := strings.ToLower(node.Data)
	dataType := strings.ToLower(htmlAttribute(node, "data-type"))
	if tag == "img" {
		return importNode{Type: "image", Attrs: htmlMediaAttributes(node, "src")}, true
	}
	if tag == "video" || tag == "audio" {
		attrs := htmlMediaAttributes(node, "src")
		if htmlAttribute(node, "src") == "" {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == htmlnode.ElementNode && strings.EqualFold(child.Data, "source") {
					if source := htmlAttribute(child, "src"); source != "" {
						attrs["src"] = source
						break
					}
				}
			}
		}
		if tag == "video" {
			return importNode{Type: "video", Attrs: attrs}, true
		}
		return importNode{Type: "audio", Attrs: attrs}, true
	}
	if tag != "div" && tag != "span" {
		return importNode{}, false
	}
	if dataType == "" {
		return importNode{}, false
	}
	switch dataType {
	case "attachment":
		attrs := htmlAttributes(node, map[string]string{
			"data-attachment-url":  "url",
			"data-attachment-name": "name",
			"data-attachment-mime": "mime",
			"data-attachment-size": "size",
			"data-attachment-id":   "attachmentId",
		})
		return importNode{Type: "attachment", Attrs: attrs}, true
	case "pdf":
		attrs := htmlAttributes(node, map[string]string{
			"src":                "src",
			"data-name":          "name",
			"data-size":          "size",
			"data-attachment-id": "attachmentId",
			"width":              "width",
			"height":             "height",
		})
		return importNode{Type: "pdf", Attrs: attrs}, true
	case "drawio", "excalidraw":
		attrs := htmlAttributes(node, map[string]string{
			"data-src":           "src",
			"data-title":         "title",
			"data-alt":           "alt",
			"data-width":         "width",
			"data-height":        "height",
			"data-align":         "align",
			"data-size":          "size",
			"data-attachment-id": "attachmentId",
		})
		return importNode{Type: dataType, Attrs: attrs}, true
	case "embed":
		attrs := htmlAttributes(node, map[string]string{
			"data-src":      "src",
			"data-provider": "provider",
			"data-width":    "width",
			"data-height":   "height",
			"data-align":    "align",
		})
		return importNode{Type: "embed", Attrs: attrs}, true
	case "mathblock":
		return importNode{Type: "mathBlock", Attrs: map[string]any{"text": htmlText(node)}}, true
	case "mathinline":
		return importNode{Type: "mathInline", Attrs: map[string]any{"text": htmlText(node)}}, true
	case "callout":
		return importNode{Type: "callout", Attrs: htmlAttributes(node, map[string]string{
			"data-callout-type": "type",
			"data-callout-icon": "icon",
		}), Content: htmlBlockChildren(node)}, true
	case "columns":
		return importNode{Type: "columns", Attrs: htmlAttributes(node, map[string]string{
			"data-layout":     "layout",
			"data-width-mode": "widthMode",
		}), Content: htmlBlockChildren(node)}, true
	case "column":
		return importNode{Type: "column", Content: htmlBlockChildren(node)}, true
	default:
		return importNode{}, false
	}
}

func htmlMediaAttributes(node *htmlnode.Node, sourceAttribute string) map[string]any {
	return htmlAttributes(node, map[string]string{
		sourceAttribute:      "src",
		"alt":                "alt",
		"aria-label":         "alt",
		"width":              "width",
		"height":             "height",
		"data-align":         "align",
		"data-size":          "size",
		"data-aspect-ratio":  "aspectRatio",
		"data-attachment-id": "attachmentId",
	})
}

func htmlAttributes(node *htmlnode.Node, mapping map[string]string) map[string]any {
	attrs := make(map[string]any, len(mapping))
	for source, destination := range mapping {
		if value := htmlAttribute(node, source); value != "" {
			attrs[destination] = value
		}
	}
	return attrs
}

func htmlAttribute(node *htmlnode.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
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
