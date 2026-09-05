package http

import (
	"archive/zip"
	"bytes"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"centipede/internal/modules/docmost/domain"

	"github.com/gin-gonic/gin"
)

func (handler *Handler) exportDocx(c *gin.Context) {
	var request struct {
		PageID string `json:"pageId"`
	}
	if !decode(c, &request) || request.PageID == "" {
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
	data, err := buildDocx(page)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "Failed to create DOCX export")
		return
	}
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	c.Header("Content-Disposition", `attachment; filename="`+url.PathEscape(safeExportFileName(exportTitle(page.Title))+`.docx"`))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", data)
}

func buildDocx(page domain.Page) ([]byte, error) {
	var body strings.Builder
	body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t xml:space="preserve">`)
	body.WriteString(xmlEscape(exportTitle(page.Title)))
	body.WriteString(`</w:t></w:r></w:p>`)
	var document exportNode
	if len(page.Content) > 0 && jsonUnmarshal(page.Content, &document) == nil {
		for _, node := range document.Content {
			body.WriteString(renderDocxBlock(node))
		}
	}
	documentXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`
	files := map[string]string{
		"[Content_Types].xml":          `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`,
		"_rels/.rels":                  `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`,
		"word/document.xml":            documentXML,
		"word/styles.xml":              docxStylesXML,
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			_ = writer.Close()
			return nil, err
		}
		if _, err = entry.Write([]byte(content)); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return archive.Bytes(), nil
}

func renderDocxBlock(node exportNode) string {
	if node.Type == "text" {
		return renderDocxRun(node)
	}
	switch node.Type {
	case "doc":
		var result strings.Builder
		for _, child := range node.Content {
			result.WriteString(renderDocxBlock(child))
		}
		return result.String()
	case "paragraph", "heading", "blockquote", "listItem", "tableCell", "tableHeader":
		var result strings.Builder
		result.WriteString(`<w:p>`)
		if node.Type == "heading" {
			result.WriteString(`<w:pPr><w:pStyle w:val="Heading` + string(rune('0'+exportAttrInt(node.Attrs, "level", 1))) + `"/></w:pPr>`)
		}
		for _, child := range node.Content {
			if child.Type == "hardBreak" {
				result.WriteString(`<w:r><w:br/></w:r>`)
			} else {
				result.WriteString(renderDocxBlock(child))
			}
		}
		result.WriteString(`</w:p>`)
		return result.String()
	case "bulletList", "orderedList":
		var result strings.Builder
		for index, child := range node.Content {
			result.WriteString(renderDocxListItem(index, child, node.Type == "orderedList"))
		}
		return result.String()
	case "codeBlock":
		return `<w:p><w:r><w:rPr><w:rStyle w:val="Code"/></w:rPr><w:t xml:space="preserve">` + xmlEscape(exportPlainText(node.Content)) + `</w:t></w:r></w:p>`
	case "horizontalRule":
		return `<w:p><w:r><w:t>---</w:t></w:r></w:p>`
	case "image":
		alt := exportAttrString(node.Attrs, "alt")
		if alt == "" {
			alt = "image"
		}
		return `<w:p><w:r><w:t>[` + xmlEscape(alt) + `]</w:t></w:r></w:p>`
	default:
		var result strings.Builder
		for _, child := range node.Content {
			result.WriteString(renderDocxBlock(child))
		}
		return result.String()
	}
}

func renderDocxListItem(index int, item exportNode, ordered bool) string {
	var result strings.Builder
	result.WriteString(`<w:p><w:pPr><w:ind w:left="720"/></w:pPr><w:r><w:t xml:space="preserve">`)
	if ordered {
		result.WriteString(strconv.Itoa(index+1) + ". ")
	} else {
		result.WriteString("• ")
	}
	result.WriteString(`</w:t></w:r>`)
	for _, block := range item.Content {
		if block.Type == "paragraph" || block.Type == "heading" || block.Type == "blockquote" {
			for _, inline := range block.Content {
				if inline.Type == "hardBreak" {
					result.WriteString(`<w:r><w:br/></w:r>`)
				} else {
					result.WriteString(renderDocxBlock(inline))
				}
			}
			continue
		}
		result.WriteString(renderDocxBlock(block))
	}
	result.WriteString(`</w:p>`)
	return result.String()
}

func renderDocxRun(node exportNode) string {
	var properties strings.Builder
	for _, mark := range node.Marks {
		switch mark.Type {
		case "bold":
			properties.WriteString(`<w:b/>`)
		case "italic":
			properties.WriteString(`<w:i/>`)
		case "strike":
			properties.WriteString(`<w:strike/>`)
		case "underline":
			properties.WriteString(`<w:u w:val="single"/>`)
		case "code":
			properties.WriteString(`<w:rStyle w:val="Code"/>`)
		}
	}
	runProperties := ""
	if properties.Len() > 0 {
		runProperties = `<w:rPr>` + properties.String() + `</w:rPr>`
	}
	return `<w:r>` + runProperties + `<w:t xml:space="preserve">` + xmlEscape(node.Text) + `</w:t></w:r>`
}

func xmlEscape(value string) string {
	return html.EscapeString(value)
}

const docxStylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:sz w:val="22"/></w:rPr></w:rPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="center"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:rPr><w:b/><w:sz w:val="28"/></w:rPr></w:style><w:style w:type="character" w:styleId="Code"><w:name w:val="Code"/><w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New"/></w:rPr></w:style></w:styles>`
