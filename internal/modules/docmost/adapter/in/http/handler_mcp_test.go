package http

import (
	"encoding/json"
	"testing"
)

func TestMCPToolsExposeExpectedNames(t *testing.T) {
	tools := mcpTools()
	wanted := map[string]bool{"search_pages": false, "get_page": false, "create_page": false, "update_page": false, "get_current_user": false}
	for _, tool := range tools {
		if _, ok := wanted[tool.Name]; ok {
			wanted[tool.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("MCP tool %q is missing", name)
		}
	}
}

func TestMCPContentAcceptsTiptapJSONAndPlainText(t *testing.T) {
	raw, err := mcpContent(`{"type":"doc","content":[]}`)
	if err != nil || string(raw) != `{"type":"doc","content":[]}` {
		t.Fatalf("unexpected JSON content: %s, %v", raw, err)
	}
	raw, err = mcpContent("hello")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil || document["type"] != "doc" {
		t.Fatalf("plain text was not converted to a document: %s", raw)
	}
}

func TestMCPScopeChecks(t *testing.T) {
	if !mcpHasScope([]string{"read", "write"}, "read") || mcpHasScope([]string{"read"}, "write") {
		t.Fatal("unexpected MCP scope result")
	}
}
