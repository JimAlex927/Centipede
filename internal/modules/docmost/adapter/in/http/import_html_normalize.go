package http

import (
	"strings"

	htmlnode "golang.org/x/net/html"
)

// normalizeImportedHTML handles the class-based HTML emitted by Notion and
// other exporters before the generic HTML-to-Tiptap walker runs. Keeping this
// as a DOM pass avoids leaking exporter-specific wrappers into page content.
func normalizeImportedHTML(root *htmlnode.Node) {
	if root == nil {
		return
	}
	for _, node := range htmlElements(root) {
		if hasHTMLClass(node, "page-header-icon") || hasHTMLClass(node, "page-cover-image") {
			removeHTMLNode(node)
			continue
		}
		if node.Data == "p" && hasHTMLClass(node, "page-description") && strings.TrimSpace(htmlText(node)) == "" {
			removeHTMLNode(node)
		}
	}
	normalizeHTMLColumns(root)
	normalizeHTMLEquations(root)
	normalizeHTMLCallouts(root)
	normalizeHTMLTodoLists(root)
	normalizeHTMLWikiContent(root)
	normalizeHTMLDetails(root)
	normalizeHTMLDefaults(root)
}

func normalizeHTMLColumns(root *htmlnode.Node) {
	for _, list := range htmlElements(root) {
		if list.Data != "div" || !hasHTMLClass(list, "column-list") || list.Parent == nil {
			continue
		}
		columns := make([]*htmlnode.Node, 0)
		for child := list.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == htmlnode.ElementNode && child.Data == "div" && hasHTMLClass(child, "column") {
				columns = append(columns, child)
			}
		}
		if len(columns) == 0 {
			continue
		}
		if len(columns) == 1 {
			replaceHTMLNodeWithChildren(list, columns[0])
			continue
		}
		wrapper := newHTMLElement("div", map[string]string{
			"data-type":   "columns",
			"data-layout": notionColumnsLayout(len(columns)),
		})
		for _, column := range columns {
			wrapperColumn := newHTMLElement("div", map[string]string{"data-type": "column"})
			for child := column.FirstChild; child != nil; {
				next := child.NextSibling
				if child.Type == htmlnode.ElementNode && child.Data == "div" && strings.Contains(strings.ToLower(htmlAttribute(child, "style")), "display:contents") {
					for nested := child.FirstChild; nested != nil; {
						nestedNext := nested.NextSibling
						child.RemoveChild(nested)
						wrapperColumn.AppendChild(nested)
						nested = nestedNext
					}
					column.RemoveChild(child)
				} else {
					column.RemoveChild(child)
					wrapperColumn.AppendChild(child)
				}
				child = next
			}
			wrapper.AppendChild(wrapperColumn)
		}
		replaceHTMLNode(list, wrapper)
	}
}

func normalizeHTMLEquations(root *htmlnode.Node) {
	for _, node := range htmlElements(root) {
		if node.Data == "figure" && hasHTMLClass(node, "equation") && node.Parent != nil {
			tex := ""
			for _, child := range htmlElements(node) {
				if child.Data == "annotation" && htmlAttribute(child, "encoding") == "application/x-tex" {
					tex = strings.TrimSpace(htmlText(child))
					break
				}
			}
			if tex != "" {
				replacement := newHTMLElement("div", map[string]string{"data-type": "mathBlock", "data-katex": "true"})
				replacement.AppendChild(&htmlnode.Node{Type: htmlnode.TextNode, Data: tex})
				replaceHTMLNode(node, replacement)
			}
		}
		if node.Data == "span" && hasHTMLClass(node, "notion-text-equation-token") && node.Parent != nil {
			tex := ""
			for _, child := range htmlElements(node) {
				if child.Data == "annotation" && htmlAttribute(child, "encoding") == "application/x-tex" {
					tex = strings.TrimSpace(htmlText(child))
					break
				}
			}
			if tex != "" {
				replacement := newHTMLElement("span", map[string]string{"data-type": "mathInline", "data-katex": "true"})
				replacement.AppendChild(&htmlnode.Node{Type: htmlnode.TextNode, Data: tex})
				replaceHTMLNode(node, replacement)
			}
		}
	}
}

func normalizeHTMLCallouts(root *htmlnode.Node) {
	for _, figure := range htmlElements(root) {
		if figure.Data != "figure" || !hasHTMLClass(figure, "callout") || figure.Parent == nil {
			continue
		}
		divs := make([]*htmlnode.Node, 0)
		for _, child := range htmlElements(figure) {
			if child.Data == "div" {
				divs = append(divs, child)
			}
		}
		if len(divs) == 0 {
			continue
		}
		content := divs[0]
		if len(divs) > 1 {
			content = divs[1]
		}
		replacement := newHTMLElement("div", map[string]string{"data-type": "callout", "data-callout-type": "info"})
		for child := content.FirstChild; child != nil; {
			next := child.NextSibling
			content.RemoveChild(child)
			replacement.AppendChild(child)
			child = next
		}
		replaceHTMLNode(figure, replacement)
	}
}

func normalizeHTMLTodoLists(root *htmlnode.Node) {
	for _, list := range htmlElements(root) {
		if list.Data != "ul" || !hasHTMLClass(list, "to-do-list") {
			continue
		}
		setHTMLAttribute(list, "data-type", "taskList")
		for _, item := range htmlElements(list) {
			if item.Data != "li" {
				continue
			}
			checked := false
			for _, child := range htmlElements(item) {
				if hasHTMLClass(child, "checkbox-on") {
					checked = true
					break
				}
			}
			setHTMLAttribute(item, "data-type", "taskItem")
			setHTMLAttribute(item, "data-checked", boolString(checked))
		}
	}
}

func normalizeHTMLWikiContent(root *htmlnode.Node) {
	for _, node := range htmlElements(root) {
		if node.Data != "div" || htmlAttribute(node, "id") != "xwikicontent" || node.Parent == nil {
			continue
		}
		parent := node.Parent
		for child := parent.FirstChild; child != nil; {
			next := child.NextSibling
			if child != node {
				parent.RemoveChild(child)
			}
			child = next
		}
		for child := node.FirstChild; child != nil; {
			next := child.NextSibling
			parent.AppendChild(child)
			child = next
		}
		parent.RemoveChild(node)
		break
	}
}

func normalizeHTMLDetails(root *htmlnode.Node) {
	for _, details := range htmlElements(root) {
		if details.Data != "details" || details.Parent == nil {
			continue
		}
		// Notion wraps toggles in a list item. Move the native details node out
		// of that wrapper so the block parser can preserve it as a top-level
		// Docmost details node instead of treating it as inline list content.
		if item := details.Parent; item.Data == "li" && item.Parent != nil && item.Parent.Data == "ul" && hasHTMLClass(item.Parent, "toggle") && item.Parent.Parent != nil {
			list := item.Parent
			container := list.Parent
			item.RemoveChild(details)
			container.InsertBefore(details, list)
			if !hasMeaningfulHTMLChild(item) {
				list.RemoveChild(item)
			}
			if !hasMeaningfulHTMLChild(list) {
				removeHTMLNode(list)
			}
		}
	}
}

func normalizeHTMLDefaults(root *htmlnode.Node) {
	for _, node := range htmlElements(root) {
		if node.Parent == nil {
			continue
		}
		if node.Data == "nav" && hasHTMLClass(node, "table_of_contents") {
			removeHTMLNode(node)
			continue
		}
		if node.Data == "img" && hasHTMLClass(node, "user-icon") {
			removeHTMLNode(node)
			continue
		}
		if node.Data == "iframe" {
			src := htmlAttribute(node, "src")
			if src == "" {
				continue
			}
			replacement := newHTMLElement("div", map[string]string{
				"data-type": "embed", "data-src": src, "data-provider": "iframe",
				"data-align": "center", "data-width": "640", "data-height": "480",
			})
			replaceHTMLNode(node, replacement)
			continue
		}
		if node.Data != "figure" || !hasHTMLClass(node, "bookmark") {
			continue
		}
		var link *htmlnode.Node
		for _, child := range htmlElements(node) {
			if child.Data == "a" && hasHTMLClass(child, "source") && htmlAttribute(child, "href") != "" {
				link = child
				break
			}
		}
		if link == nil {
			continue
		}
		title := htmlText(link)
		for _, child := range htmlElements(link) {
			if hasHTMLClass(child, "bookmark-title") && strings.TrimSpace(htmlText(child)) != "" {
				title = htmlText(child)
				break
			}
		}
		replacement := newHTMLElement("p", nil)
		anchor := newHTMLElement("a", map[string]string{"href": htmlAttribute(link, "href")})
		anchor.AppendChild(&htmlnode.Node{Type: htmlnode.TextNode, Data: strings.TrimSpace(title)})
		replacement.AppendChild(anchor)
		replaceHTMLNode(node, replacement)
	}
}

func hasMeaningfulHTMLChild(node *htmlnode.Node) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == htmlnode.ElementNode || (child.Type == htmlnode.TextNode && strings.TrimSpace(child.Data) != "") {
			return true
		}
	}
	return false
}

func notionColumnsLayout(count int) string {
	switch count {
	case 3:
		return "three_equal"
	case 4:
		return "four_equal"
	case 5:
		return "five_equal"
	default:
		return "two_equal"
	}
}

func htmlElements(root *htmlnode.Node) []*htmlnode.Node {
	result := make([]*htmlnode.Node, 0)
	var walk func(*htmlnode.Node)
	walk = func(node *htmlnode.Node) {
		if node.Type == htmlnode.ElementNode {
			result = append(result, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func hasHTMLClass(node *htmlnode.Node, wanted string) bool {
	for _, value := range strings.Fields(htmlAttribute(node, "class")) {
		if value == wanted {
			return true
		}
	}
	return false
}

func newHTMLElement(tag string, attributes map[string]string) *htmlnode.Node {
	node := &htmlnode.Node{Type: htmlnode.ElementNode, Data: tag}
	for key, value := range attributes {
		node.Attr = append(node.Attr, htmlnode.Attribute{Key: key, Val: value})
	}
	return node
}

func setHTMLAttribute(node *htmlnode.Node, key, value string) {
	for index := range node.Attr {
		if strings.EqualFold(node.Attr[index].Key, key) {
			node.Attr[index].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, htmlnode.Attribute{Key: key, Val: value})
}

func removeHTMLNode(node *htmlnode.Node) {
	if node != nil && node.Parent != nil {
		node.Parent.RemoveChild(node)
	}
}

func replaceHTMLNode(old, replacement *htmlnode.Node) {
	if old == nil || old.Parent == nil || replacement == nil {
		return
	}
	old.Parent.InsertBefore(replacement, old)
	old.Parent.RemoveChild(old)
}

func replaceHTMLNodeWithChildren(old, source *htmlnode.Node) {
	if old == nil || old.Parent == nil || source == nil {
		return
	}
	parent := old.Parent
	for child := source.FirstChild; child != nil; {
		next := child.NextSibling
		source.RemoveChild(child)
		parent.InsertBefore(child, old)
		child = next
	}
	parent.RemoveChild(old)
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
