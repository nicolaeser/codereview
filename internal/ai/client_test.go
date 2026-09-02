package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/logx"
)

func TestCrossHostRedirectStripsAuthorization(t *testing.T) {
	tests := []struct {
		name      string
		authMode  string
		header    string
		headerKey string
	}{
		{name: "bearer", authMode: "bearer", header: "Authorization", headerKey: ""},
		{name: "x-api-key", authMode: "x-api-key", header: "X-Api-Key", headerKey: ""},
		{name: "custom api-key header", authMode: "api-key", header: "X-Custom-Key", headerKey: "X-Custom-Key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var destValue string
			dest := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				destValue = request.Header.Get(test.header)
				writeAICompletion(response, `{"summary":"ok"}`)
			}))
			t.Cleanup(dest.Close)

			origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				http.Redirect(response, request, dest.URL+request.URL.Path, http.StatusFound)
			}))
			t.Cleanup(origin.Close)

			client := New(config.Config{AI: config.AIConfig{
				Provider:            "openai",
				BaseURL:             origin.URL + "/v1",
				APIKey:              "super-secret-key",
				AuthMode:            test.authMode,
				APIKeyHeader:        test.headerKey,
				Models:              []string{"test-model"},
				ChatCompletionsPath: "/chat/completions",
				Timeout:             time.Second,
				MaxRetries:          0,
				MaxCompletions:      8,
				MaxTokens:           100,
				MaxTokensField:      "max_tokens",
			}}, logx.New(io.Discard, "error"))
			if _, err := client.Complete(context.Background(), Request{System: "s", User: "u"}); err != nil {
				t.Fatal(err)
			}
			if destValue != "" {
				t.Fatalf("cross-host redirect leaked %s=%q", test.header, destValue)
			}
		})
	}
}

func TestMaxCompletionsStopsFurtherHTTPCalls(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		http.Error(response, `{"error":"upstream"}`, http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	client := New(config.Config{AI: config.AIConfig{
		Provider:            "openai",
		BaseURL:             server.URL + "/v1",
		APIKey:              "key",
		AuthMode:            "bearer",
		Models:              []string{"test-model"},
		ChatCompletionsPath: "/chat/completions",
		Timeout:             time.Second,
		MaxRetries:          0,
		MaxCompletions:      2,
		MaxTokens:           100,
		MaxTokensField:      "max_tokens",
	}}, logx.New(io.Discard, "error"))

	for i := 0; i < 2; i++ {
		if _, err := client.Complete(context.Background(), Request{System: "s", User: "u"}); err == nil {
			t.Fatal("expected provider error before the completion cap")
		}
	}
	if calls != 2 {
		t.Fatalf("HTTP calls = %d, want 2", calls)
	}

	_, err := client.Complete(context.Background(), Request{System: "s", User: "u"})
	if err == nil || !strings.Contains(err.Error(), "AI_MAX_COMPLETIONS") {
		t.Fatalf("Complete() after cap error = %v, want AI_MAX_COMPLETIONS", err)
	}
	if calls != 2 {
		t.Fatalf("HTTP calls after cap = %d, want 2", calls)
	}
}

func TestMaxCompletionsCountsRetries(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.Header().Set("Retry-After", "0")
		http.Error(response, `{"error":"upstream"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	client := New(config.Config{AI: config.AIConfig{
		Provider:            "openai",
		BaseURL:             server.URL + "/v1",
		APIKey:              "key",
		AuthMode:            "bearer",
		Models:              []string{"test-model"},
		ChatCompletionsPath: "/chat/completions",
		Timeout:             time.Second,
		MaxRetries:          5,
		MaxCompletions:      3,
		MaxTokens:           100,
		MaxTokensField:      "max_tokens",
	}}, logx.New(io.Discard, "error"))

	_, err := client.Complete(context.Background(), Request{System: "s", User: "u"})
	if err == nil || !strings.Contains(err.Error(), "AI_MAX_COMPLETIONS") {
		t.Fatalf("Complete() error = %v, want AI_MAX_COMPLETIONS", err)
	}
	if calls != 3 {
		t.Fatalf("HTTP calls = %d, want 3 (retries must consume the cap)", calls)
	}
}

func writeAICompletion(response http.ResponseWriter, content string) {
	response.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(response, `{"choices":[{"message":{"content":`+jsonQuote(content)+`}}]}`)
}

func jsonQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
