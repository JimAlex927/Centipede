package yjs

import (
	"encoding/json"
	"testing"
)

func TestLoadAndJSONRoundTrip(t *testing.T) {
	input := []byte(`{"type":"doc","content":[{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Hello"}]},{"type":"paragraph","content":[{"type":"text","text":"world","marks":[{"type":"bold","attrs":{}}]}]}]}`)
	doc, err := Load(nil, input)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	output, plainText, err := JSON(doc)
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	if plainText != "Hello\nworld\n" {
		t.Fatalf("plain text = %q", plainText)
	}
	var got Node
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(got.Content) != 2 || got.Content[0].Type != "heading" || got.Content[1].Type != "paragraph" {
		t.Fatalf("unexpected nodes: %#v", got.Content)
	}
	if len(got.Content[1].Content) != 1 || len(got.Content[1].Content[0].Marks) != 1 || got.Content[1].Content[0].Marks[0].Type != "bold" {
		t.Fatalf("marks were not projected: %#v", got.Content[1].Content[0])
	}
}
