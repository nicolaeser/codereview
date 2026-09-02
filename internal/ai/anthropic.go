package ai

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nicolaeser/codereview/internal/config"
)

type anthropicClient struct {
	cfg  config.AIConfig
	http *http.Client
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (c *anthropicClient) complete(ctx context.Context, model string, input Request) (string, error) {
	target, err := endpoint(c.cfg.BaseURL, c.cfg.AnthropicMessagesPath)
	if err != nil {
		return "", err
	}
	payload := anthropicRequest{
		Model:       model,
		System:      input.System,
		Messages:    []anthropicMessage{{Role: "user", Content: input.User}},
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: c.cfg.Temperature,
	}
	request, err := newJSONRequest(ctx, http.MethodPost, target, payload)
	if err != nil {
		return "", err
	}
	applyAuth(request, c.cfg.AuthMode, c.cfg.APIKey, c.cfg.APIKeyHeader)
	request.Header.Set("anthropic-version", c.cfg.AnthropicVersion)
	for key, value := range c.cfg.ExtraHeaders {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	var decoded anthropicResponse
	if err := decodeOrError(response, &decoded); err != nil {
		return "", err
	}
	var result strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" && block.Text != "" {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(block.Text)
		}
	}
	if result.Len() == 0 {
		return "", errors.New("Anthropic response contains no text block")
	}
	return result.String(), nil
}
