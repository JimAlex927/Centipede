package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAIProviderCompleteUsesOpenAICompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization header was not forwarded")
		}
		var body struct {
			Model    string      `json:"model"`
			Messages []AIMessage `json:"messages"`
			Stream   bool        `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "test-model" || body.Stream || len(body.Messages) != 1 || body.Messages[0].Content != "hello" {
			t.Fatalf("unexpected request: %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"world"}}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))
	}))
	defer server.Close()

	provider := NewAIProvider(server.URL+"/v1", "secret", "test-model", time.Second)
	result, err := provider.Complete(context.Background(), []AIMessage{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result.Content != "world" || result.TotalTokens != 3 {
		t.Fatalf("unexpected completion: %+v", result)
	}
}

func TestAIProviderUsesDedicatedCompletionModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "completion-model" {
			t.Fatalf("model = %q, want completion-model", body.Model)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := NewAIProviderWithModels(server.URL+"/v1", "", "completion-model", "chat-model", time.Second)
	if provider.CompletionModel() != "completion-model" {
		t.Fatalf("completion model = %q", provider.CompletionModel())
	}
	if _, err := provider.CompleteWithModel(context.Background(), []AIMessage{{Role: "user", Content: "hello"}}, provider.CompletionModel()); err != nil {
		t.Fatalf("complete with dedicated model: %v", err)
	}
}

func TestAIProviderEmbedUsesOpenAICompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "embedding-model" || len(body.Input) != 2 {
			t.Fatalf("unexpected request: %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`))
	}))
	defer server.Close()

	provider := NewAIProviderWithModelsAndEmbedding(server.URL+"/v1", "", "completion-model", "chat-model", "embedding-model", time.Second)
	result, err := provider.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(result) != 2 || result[0].Index != 0 || result[1].Index != 1 || result[0].Embedding[0] != 0.1 {
		t.Fatalf("unexpected embeddings: %+v", result)
	}
}

func TestAIProviderRequiresConfiguration(t *testing.T) {
	provider := NewAIProvider("", "", "", time.Second)
	if provider.Configured() {
		t.Fatal("expected provider to be disabled")
	}
	_, err := provider.Complete(context.Background(), nil)
	if err != ErrAIProviderNotConfigured {
		t.Fatalf("error = %v", err)
	}
	if strings.TrimSpace(ErrAIProviderNotConfigured.Error()) == "" {
		t.Fatal("expected descriptive configuration error")
	}
}

func TestAIProviderCompleteWithToolsUsesNestedToolCallContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
			Tools    []AITool         `json:"tools"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "search_pages" {
			t.Fatalf("unexpected tools: %+v", body.Tools)
		}
		if len(body.Messages) != 2 {
			t.Fatalf("messages = %d, want 2", len(body.Messages))
		}
		assistantCalls, ok := body.Messages[1]["tool_calls"].([]any)
		if !ok || len(assistantCalls) != 1 {
			t.Fatalf("unexpected assistant tool calls: %#v", body.Messages[1]["tool_calls"])
		}
		call, ok := assistantCalls[0].(map[string]any)
		if !ok || call["id"] != "call-previous" {
			t.Fatalf("unexpected nested tool call: %#v", assistantCalls[0])
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"search_pages","arguments":"{\"query\":\"hello\"}"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	provider := NewAIProvider(server.URL+"/v1", "", "test-model", time.Second)
	result, err := provider.CompleteWithTools(context.Background(), []AIMessage{
		{Role: "user", Content: "find hello"},
		{Role: "assistant", ToolCalls: []AIToolCall{{ID: "call-previous", Name: "list_spaces", Arguments: `{}`}}},
	}, []AITool{{Type: "function", Function: AIToolFunction{
		Name: "search_pages", Description: "Search pages", Parameters: map[string]any{"type": "object"},
	}}})
	if err != nil {
		t.Fatalf("complete with tools: %v", err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-1" || result.ToolCalls[0].Name != "search_pages" {
		t.Fatalf("unexpected tool calls: %+v", result.ToolCalls)
	}
	if result.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want 3", result.TotalTokens)
	}
}
