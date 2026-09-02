package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nicolaeser/codereview/internal/config"
)

func TestOpenRouterPrivacyFields(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/chat/completions" {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("missing bearer authorization")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		provider := body["provider"].(map[string]any)
		if provider["zdr"] != true || provider["data_collection"] != "deny" {
			t.Errorf("privacy routing missing: %#v", provider)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader("{\"choices\":[{\"message\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\"}\"}}]}")),
		}, nil
	})}

	client := &openAIClient{
		cfg:        config.AIConfig{BaseURL: "https://example.test/api/v1", APIKey: "key", AuthMode: "bearer", ChatCompletionsPath: "/chat/completions", MaxTokens: 100, MaxTokensField: "max_tokens"},
		openRouter: config.OpenRouterConfig{ZDR: true, DataCollection: "deny", IncludeZDR: true, IncludeDataCollection: true, AllowFallbacks: true},
		provider:   "openrouter",
		http:       httpClient,
	}
	result, err := client.complete(context.Background(), "provider/model", Request{System: "system", User: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "{\"summary\":\"ok\"}" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestOpenRouterOmitsPrivacyWhenNotIncluded(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		provider := body["provider"].(map[string]any)
		if _, ok := provider["zdr"]; ok {
			t.Errorf("zdr must be omitted: %#v", provider)
		}
		if _, ok := provider["data_collection"]; ok {
			t.Errorf("data_collection must be omitted: %#v", provider)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader("{\"choices\":[{\"message\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\"}\"}}]}")),
		}, nil
	})}
	client := &openAIClient{
		cfg:        config.AIConfig{BaseURL: "https://eu.openrouter.ai/api/v1", APIKey: "key", AuthMode: "bearer", ChatCompletionsPath: "/chat/completions", MaxTokens: 100, MaxTokensField: "max_tokens"},
		openRouter: config.OpenRouterConfig{AllowFallbacks: true},
		provider:   "openrouter",
		http:       httpClient,
	}
	if _, err := client.complete(context.Background(), "provider/model", Request{System: "system", User: "user"}); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointRejectsAbsolutePath(t *testing.T) {
	if _, err := endpoint("https://example.test/api/v1", "https://evil.example.test/chat/completions"); err == nil {
		t.Fatal("expected absolute endpoint path to be rejected")
	}
}

func TestAPIErrorStringOmitsBody(t *testing.T) {
	err := (&APIError{Status: http.StatusBadGateway, Body: "sensitive upstream text"}).Error()
	if strings.Contains(err, "sensitive upstream text") {
		t.Fatalf("API error leaked body content: %q", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
