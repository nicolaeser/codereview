package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nicolaeser/codereview/internal/config"
)

type openAIClient struct {
	cfg        config.AIConfig
	openRouter config.OpenRouterConfig
	provider   string
	http       *http.Client
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model          string                 `json:"model"`
	Messages       []openAIMessage        `json:"messages"`
	Temperature    float64                `json:"temperature,omitempty"`
	MaxTokens      int                    `json:"max_tokens,omitempty"`
	MaxCompletion  int                    `json:"max_completion_tokens,omitempty"`
	Stream         bool                   `json:"stream"`
	ResponseFormat map[string]string      `json:"response_format,omitempty"`
	Provider       *openRouterPreferences `json:"provider,omitempty"`
}

type openRouterPreferences struct {
	ZDR               *bool    `json:"zdr,omitempty"`
	DataCollection    string   `json:"data_collection,omitempty"`
	AllowFallbacks    bool     `json:"allow_fallbacks"`
	RequireParameters bool     `json:"require_parameters"`
	Order             []string `json:"order,omitempty"`
	Only              []string `json:"only,omitempty"`
	Ignore            []string `json:"ignore,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *openAIClient) complete(ctx context.Context, model string, input Request) (string, error) {
	payload := openAIRequest{
		Model:       model,
		Messages:    []openAIMessage{{Role: "system", Content: input.System}, {Role: "user", Content: input.User}},
		Temperature: c.cfg.Temperature,
		Stream:      false,
	}
	if c.cfg.MaxTokensField == "max_completion_tokens" {
		payload.MaxCompletion = c.cfg.MaxTokens
	} else {
		payload.MaxTokens = c.cfg.MaxTokens
	}
	if c.cfg.JSONMode == "json_object" {
		payload.ResponseFormat = map[string]string{"type": "json_object"}
	}
	if c.provider == "openrouter" {
		prefs := &openRouterPreferences{
			AllowFallbacks:    c.openRouter.AllowFallbacks,
			RequireParameters: c.openRouter.RequireParameters,
			Order:             c.openRouter.ProviderOrder,
			Only:              c.openRouter.OnlyProviders,
			Ignore:            c.openRouter.IgnoredProviders,
		}
		if c.openRouter.IncludeZDR {
			flag := c.openRouter.ZDR
			prefs.ZDR = &flag
		}
		if c.openRouter.IncludeDataCollection {
			prefs.DataCollection = c.openRouter.DataCollection
		}
		payload.Provider = prefs
	}

	target, err := endpoint(c.cfg.BaseURL, c.cfg.ChatCompletionsPath)
	if err != nil {
		return "", err
	}
	request, err := newJSONRequest(ctx, http.MethodPost, target, payload)
	if err != nil {
		return "", err
	}
	applyAuth(request, c.cfg.AuthMode, c.cfg.APIKey, c.cfg.APIKeyHeader)
	for key, value := range c.cfg.ExtraHeaders {
		request.Header.Set(key, value)
	}
	if c.provider == "openrouter" {
		if c.openRouter.SiteURL != "" {
			request.Header.Set("HTTP-Referer", c.openRouter.SiteURL)
		}
		if c.openRouter.AppName != "" {
			request.Header.Set("X-Title", c.openRouter.AppName)
		}
		if c.openRouter.RouterMetadata {
			request.Header.Set("X-OpenRouter-Metadata", "enabled")
		}
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	var decoded openAIResponse
	if err := decodeOrError(response, &decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("AI response contains no choices")
	}
	text, err := contentText(decoded.Choices[0].Message.Content)
	if err != nil {
		return "", fmt.Errorf("decode message content: %w", err)
	}
	if text == "" {
		return "", errors.New("AI response content is empty")
	}
	return text, nil
}
