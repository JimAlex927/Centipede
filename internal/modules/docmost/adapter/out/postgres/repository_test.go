package postgres

import (
	"encoding/json"
	"testing"
)

func TestJSONTextContent(t *testing.T) {
	got := jsonTextContent([]byte(`{"type":"doc","content":[{"type":"heading","content":[{"type":"text","text":"Title"}]},{"type":"paragraph","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}]}`))
	if got != "Title\nHello world" {
		t.Fatalf("got %q", got)
	}
}

func TestBacklinkTargets(t *testing.T) {
	content := json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"mention","attrs":{"entityType":"page","entityId":"target"}},{"type":"text","marks":[{"type":"link","attrs":{"internal":true,"href":"/s/main/p/linked-page"}}]}]}]}`)
	ids, slugs := backlinkTargets(content, "source")
	if len(ids) != 1 || ids[0] != "target" {
		t.Fatalf("unexpected page mention targets: %#v", ids)
	}
	if len(slugs) != 1 || slugs[0] != "linked-page" {
		t.Fatalf("unexpected internal link slugs: %#v", slugs)
	}
}
