package http

import (
	"encoding/json"
	"reflect"
	"strings"
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

func TestRewriteTransclusionAttachments(t *testing.T) {
	raw := []byte(`[{"type":"image","attrs":{"attachmentId":"old","src":"/api/files/old/image.png"}},{"type":"attachment","attrs":{"attachmentId":"old","url":"/api/files/old/image.png"}}]`)
	rewritten, copies, err := rewriteTransclusionAttachments(raw)
	if err != nil || len(copies) != 1 || copies[0].OldID != "old" {
		t.Fatalf("unexpected rewrite plan: copies=%#v err=%v", copies, err)
	}
	var value []map[string]any
	if err := json.Unmarshal(rewritten, &value); err != nil {
		t.Fatal(err)
	}
	newID := copies[0].NewID
	if value[0]["attrs"].(map[string]any)["attachmentId"] != newID || value[1]["attrs"].(map[string]any)["attachmentId"] != newID {
		t.Fatalf("attachment ids were not shared: %s", rewritten)
	}
	if !strings.Contains(value[0]["attrs"].(map[string]any)["src"].(string), newID) {
		t.Fatalf("attachment src was not rewritten: %s", rewritten)
	}
}
