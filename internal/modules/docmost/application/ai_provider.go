package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrAIProviderNotConfigured = errors.New("AI provider is not configured")

type AIMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []AIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	Name       string       `json:"name,omitempty"`
}

type AITool struct {
	Type     string         `json:"type"`
	Function AIToolFunction `json:"function"`
}

type AIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type AIToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// MarshalJSON keeps the internal tool-call representation small while
// emitting the nested shape required for an assistant tool-call message in
// the OpenAI-compatible API.
func (message AIMessage) MarshalJSON() ([]byte, error) {
	payload := map[string]any{"role": message.Role}
	if message.Content != "" || (message.Role == "assistant" && len(message.ToolCalls) > 0) {
		payload["content"] = message.Content
	}
	if len(message.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, map[string]any{
				"id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
			})
		}
		payload["tool_calls"] = calls
	}
	if message.ToolCallID != "" {
		payload["tool_call_id"] = message.ToolCallID
	}
	if message.Name != "" {
		payload["name"] = message.Name
	}
	return json.Marshal(payload)
}

type AICompletion struct {
	Content          string       `json:"content"`
	ToolCalls        []AIToolCall `json:"toolCalls,omitempty"`
	PromptTokens     int          `json:"promptTokens"`
	CompletionTokens int          `json:"completionTokens"`
	TotalTokens      int          `json:"totalTokens"`
}

type AIProvider struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewAIProvider(baseURL, apiKey, model string, timeout time.Duration) *AIProvider {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &AIProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		model:   strings.TrimSpace(model),
		client:  &http.Client{Timeout: timeout},
	}
}

func (provider *AIProvider) Configured() bool {
	return provider != nil && provider.baseURL != "" && provider.model != ""
}

type aiCompletionRequest struct {
	Model    string      `json:"model"`
	Messages []AIMessage `json:"messages"`
	Tools    []AITool    `json:"tools,omitempty"`
	Stream   bool        `json:"stream"`
}

type aiCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (provider *AIProvider) Complete(ctx context.Context, messages []AIMessage) (AICompletion, error) {
	return provider.complete(ctx, messages, nil)
}

func (provider *AIProvider) CompleteWithTools(ctx context.Context, messages []AIMessage, tools []AITool) (AICompletion, error) {
	return provider.complete(ctx, messages, tools)
}

func (provider *AIProvider) complete(ctx context.Context, messages []AIMessage, tools []AITool) (AICompletion, error) {
	if !provider.Configured() {
		return AICompletion{}, ErrAIProviderNotConfigured
	}
	payload, err := json.Marshal(aiCompletionRequest{Model: provider.model, Messages: messages, Tools: tools, Stream: false})
	if err != nil {
		return AICompletion{}, fmt.Errorf("encode AI request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return AICompletion{}, fmt.Errorf("create AI request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if provider.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+provider.apiKey)
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return AICompletion{}, fmt.Errorf("AI provider request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return AICompletion{}, fmt.Errorf("read AI provider response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return AICompletion{}, fmt.Errorf("AI provider returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded aiCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return AICompletion{}, fmt.Errorf("decode AI provider response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return AICompletion{}, errors.New("AI provider returned an empty response")
	}
	toolCalls := make([]AIToolCall, 0, len(decoded.Choices[0].Message.ToolCalls))
	for _, call := range decoded.Choices[0].Message.ToolCalls {
		if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
			continue
		}
		toolCalls = append(toolCalls, AIToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	if strings.TrimSpace(decoded.Choices[0].Message.Content) == "" && len(toolCalls) == 0 {
		return AICompletion{}, errors.New("AI provider returned an empty response")
	}
	return AICompletion{
		Content: decoded.Choices[0].Message.Content, ToolCalls: toolCalls, PromptTokens: decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: decoded.Usage.TotalTokens,
	}, nil
}
