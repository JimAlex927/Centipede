// Package yjs adapts the Yjs XML format used by Tiptap to persisted JSON.
package yjs

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/reearth/ygo/crdt"
)

type Node struct {
	Type    string         `json:"type"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []Node         `json:"content,omitempty"`
	Text    string         `json:"text,omitempty"`
	Marks   []Mark         `json:"marks,omitempty"`
}
type Mark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

func Load(state, content []byte) (*crdt.Doc, error) {
	doc := crdt.New()
	if len(state) > 0 {
		return doc, crdt.ApplyUpdateV1(doc, state, nil)
	}
	if len(content) == 0 || string(content) == "null" {
		return doc, nil
	}
	var root Node
	if err := json.Unmarshal(content, &root); err != nil {
		return nil, err
	}
	if root.Type != "doc" {
		return nil, errors.New("invalid document root")
	}
	frag := doc.GetXmlFragment("default")
	err := doc.TransactE(func(tx *crdt.Transaction) error { return insert(tx, frag, root.Content, 0) })
	return doc, err
}

func insert(tx *crdt.Transaction, parent *crdt.YXmlFragment, nodes []Node, depth int) error {
	if depth > 128 {
		return errors.New("document nesting too deep")
	}
	var text *crdt.YXmlText
	for _, n := range nodes {
		if n.Type == "text" {
			if text == nil {
				text = crdt.NewYXmlText()
				parent.InsertText(tx, parent.Len(), text)
			}
			attrs := crdt.Attributes{}
			for _, m := range n.Marks {
				a := m.Attrs
				if a == nil {
					a = map[string]any{}
				}
				attrs[m.Type] = a
			}
			text.Insert(tx, text.Len(), n.Text, attrs)
			continue
		}
		text = nil
		if n.Type == "" {
			return errors.New("missing node type")
		}
		elem := crdt.NewYXmlElement(n.Type)
		parent.InsertElement(tx, parent.Len(), elem)
		keys := make([]string, 0, len(n.Attrs))
		for k := range n.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			elem.SetAttributeValue(tx, k, n.Attrs[k])
		}
		if err := insert(tx, &elem.YXmlFragment, n.Content, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func JSON(doc *crdt.Doc) ([]byte, string, error) {
	var text strings.Builder
	count := 0
	nodes, err := children(doc.GetXmlFragment("default"), 0, &count, &text)
	if err != nil {
		return nil, "", err
	}
	result, err := json.Marshal(Node{Type: "doc", Content: nodes})
	return result, text.String(), err
}

func children(f *crdt.YXmlFragment, depth int, count *int, text *strings.Builder) ([]Node, error) {
	if depth > 128 {
		return nil, errors.New("document nesting too deep")
	}
	result := []Node{}
	for _, child := range f.Children() {
		*count++
		if *count > 100000 {
			return nil, errors.New("document too large")
		}
		switch n := child.(type) {
		case *crdt.YXmlElement:
			nodes, err := children(&n.YXmlFragment, depth+1, count, text)
			if err != nil {
				return nil, err
			}
			result = append(result, Node{Type: n.NodeName, Attrs: n.GetAttributeValues(), Content: nodes})
			if n.NodeName == "paragraph" || n.NodeName == "heading" || n.NodeName == "hardBreak" {
				text.WriteString("\n")
			}
		case *crdt.YXmlText:
			for _, delta := range n.ToDelta() {
				if delta.Op != crdt.DeltaOpInsert {
					continue
				}
				value, ok := delta.Insert.(string)
				if !ok {
					return nil, errors.New("unsupported text embed")
				}
				if value == "" {
					continue
				}
				node := Node{Type: "text", Text: value}
				keys := make([]string, 0, len(delta.Attributes))
				for k := range delta.Attributes {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					if delta.Attributes[k] == nil {
						continue
					}
					attrs, ok := delta.Attributes[k].(crdt.Attributes)
					if !ok {
						attrsMap, _ := delta.Attributes[k].(map[string]any)
						node.Marks = append(node.Marks, Mark{Type: strings.Split(k, "--")[0], Attrs: attrsMap})
						continue
					}
					node.Marks = append(node.Marks, Mark{Type: strings.Split(k, "--")[0], Attrs: map[string]any(attrs)})
				}
				result = append(result, node)
				text.WriteString(value)
			}
		default:
			return nil, errors.New("unsupported XML node")
		}
	}
	return result, nil
}
