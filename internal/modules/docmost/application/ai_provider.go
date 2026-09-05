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
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AICompletion struct {
	Content          string `json:"content"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	TotalTokens      int    `json:"totalTokens"`
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
	Stream   bool        `json:"stream"`
}

type aiCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func (provider *AIProvider) Complete(ctx context.Context, messages []AIMessage) (AICompletion, error) {
	if !provider.Configured() {
		return AICompletion{}, ErrAIProviderNotConfigured
	}
	payload, err := json.Marshal(aiCompletionRequest{Model: provider.model, Messages: messages, Stream: false})
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
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return AICompletion{}, errors.New("AI provider returned an empty response")
	}
	return AICompletion{
		Content: decoded.Choices[0].Message.Content, PromptTokens: decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: decoded.Usage.TotalTokens,
	}, nil
}
