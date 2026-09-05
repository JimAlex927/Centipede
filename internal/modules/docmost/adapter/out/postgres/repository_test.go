package postgres

import (
	"testing"
)

func TestJSONTextContent(t *testing.T) {
	got := jsonTextContent([]byte(`{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"Title"}]},{"type":"paragraph","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}]}`))
	if got != "Title\nHello world" {
		t.Fatalf("got %q", got)
	}
}
