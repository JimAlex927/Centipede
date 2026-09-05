package http

import "testing"

func TestAIDocumentText(t *testing.T) {
	raw := []byte(`{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"Title"}]},{"type":"paragraph","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}]}`)
	if got, want := aiDocumentText(raw), "Title\nHello world"; got != want {
		t.Fatalf("aiDocumentText() = %q, want %q", got, want)
	}
}

func TestTruncateAIContext(t *testing.T) {
	if got := truncateAIContext("  abc  ", 10); got != "abc" {
		t.Fatalf("truncateAIContext() = %q", got)
	}
	if got := truncateAIContext("abcdefgh", 4); got != "abcd\n[context truncated]" {
		t.Fatalf("truncateAIContext() = %q", got)
	}
}
