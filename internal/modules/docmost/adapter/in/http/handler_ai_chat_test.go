package http

import (
	"context"
	"encoding/json"
	"testing"

	"centipede/internal/modules/docmost/application"
	"centipede/internal/modules/docmost/domain"
)

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

func TestAIChatToolsIncludePageWrites(t *testing.T) {
	tools := aiChatTools()
	byName := make(map[string]application.AIToolFunction, len(tools))
	for _, tool := range tools {
		byName[tool.Function.Name] = tool.Function
	}
	for _, name := range []string{"list_spaces", "search_pages", "get_page", "create_page", "update_page"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("AI tool %q is missing", name)
		}
	}
	if got := byName["create_page"].Parameters["required"]; got == nil {
		t.Fatal("create_page has no required arguments")
	}
	if got := byName["update_page"].Parameters["required"]; got == nil {
		t.Fatal("update_page has no required arguments")
	}
}

func TestStringArgumentDoesNotCoerceValues(t *testing.T) {
	args := map[string]any{"title": 42, "valid": " Page "}
	if got := stringArgument(args, "title"); got != "" {
		t.Fatalf("non-string argument was coerced to %q", got)
	}
	if got := stringArgument(args, "valid"); got != " Page " {
		t.Fatalf("string argument changed to %q", got)
	}
}

func TestAIChatReadOnlyBlocksPageWrites(t *testing.T) {
	handler := &Handler{}
	current := principal{Workspace: domain.Workspace{
		Settings: json.RawMessage(`{"ai":{"chatReadOnly":true}}`),
	}}

	for _, name := range []string{"create_page", "update_page"} {
		_, err := handler.executeAIChatTool(context.Background(), current, name, map[string]any{})
		if err == nil || err.Error() != "AI Chat is in read-only mode" {
			t.Fatalf("tool %q error = %v, want read-only error", name, err)
		}
	}
}
