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
