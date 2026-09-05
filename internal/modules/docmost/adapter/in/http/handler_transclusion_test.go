package http

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTransclusionContent(t *testing.T) {
	raw := []byte(`{"type":"doc","content":[{"type":"column","content":[{"type":"transclusionSource","attrs":{"id":"block-1"},"content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}]}]}`)
	content, found := transclusionContent(raw, "block-1")
	var got, want any
	_ = json.Unmarshal(content, &got)
	_ = json.Unmarshal([]byte(`[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]`), &want)
	if !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected transclusion content: found=%v content=%s", found, content)
	}
}

func TestTransclusionContentDoesNotDescendIntoSource(t *testing.T) {
	raw := []byte(`{"type":"doc","content":[{"type":"transclusionSource","attrs":{"id":"outer"},"content":[{"type":"transclusionSource","attrs":{"id":"inner"}}]}]}`)
	if _, found := transclusionContent(raw, "inner"); found {
		t.Fatal("nested source should not be exposed as a separate transclusion")
	}
}
