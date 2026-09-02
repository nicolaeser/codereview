package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxErrorBody    = 32 * 1024
	maxRedirectHops = 5
)

func endpoint(baseURL, path string) (string, error) {
	if absolute, err := url.Parse(path); err == nil && absolute.IsAbs() {
		return "", fmt.Errorf("absolute request paths are not allowed: %s", path)
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/") + "/")
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(strings.TrimLeft(path, "/"))
	if err != nil {
		return "", err
	}
	return base.ResolveReference(reference).String(), nil
}

func newJSONRequest(ctx context.Context, method, target string, payload any) (*http.Request, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(payload); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func redirectWithoutCrossHostAuth(extraAuthHeaders ...string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirectHops {
			return fmt.Errorf("stopped after %d redirects", maxRedirectHops)
		}
		if len(via) == 0 {
			return nil
		}
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			req.Header.Del("Authorization")
			req.Header.Del("PRIVATE-TOKEN")
			req.Header.Del("JOB-TOKEN")
			req.Header.Del("X-Api-Key")
			for _, name := range extraAuthHeaders {
				if strings.TrimSpace(name) != "" {
					req.Header.Del(name)
				}
			}
		}
		return nil
	}
}

func applyAuth(request *http.Request, mode, key, apiKeyHeader string) {
	switch mode {
	case "bearer":
		request.Header.Set("Authorization", "Bearer "+key)
	case "x-api-key":
		request.Header.Set("x-api-key", key)
	case "api-key":
		request.Header.Set(apiKeyHeader, key)
	}
}

func decodeOrError(response *http.Response, target any) error {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		return &APIError{Status: response.StatusCode, Body: string(body), RetryAfter: retryAfter(response)}
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode AI response: %w", err)
	}
	return nil
}

func contentText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Text != "" {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(block.Text)
		}
	}
	return result.String(), nil
}
