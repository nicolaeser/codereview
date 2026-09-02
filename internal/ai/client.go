package ai

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/logx"
)

type Request struct {
	System string
	User   string
}

type Client interface {
	Complete(context.Context, Request) (string, error)
}

type modelClient interface {
	complete(context.Context, string, Request) (string, error)
}

type fallbackClient struct {
	client         modelClient
	models         []string
	retries        int
	maxCompletions int
	mu             sync.Mutex
	completions    int
	logger         *logx.Logger
}

func New(cfg config.Config, logger *logx.Logger) Client {
	httpClient := &http.Client{
		Timeout:       cfg.AI.Timeout,
		CheckRedirect: redirectWithoutCrossHostAuth(cfg.AI.APIKeyHeader),
	}
	var base modelClient
	if cfg.AI.Provider == "anthropic" {
		base = &anthropicClient{cfg: cfg.AI, http: httpClient}
	} else {
		base = &openAIClient{cfg: cfg.AI, openRouter: cfg.OpenRouter, provider: cfg.AI.Provider, http: httpClient}
	}
	return &fallbackClient{client: base, models: cfg.AI.Models, retries: cfg.AI.MaxRetries, maxCompletions: cfg.AI.MaxCompletions, logger: logger}
}

func (c *fallbackClient) Complete(ctx context.Context, request Request) (string, error) {
	var allErrors []error
	for _, model := range c.models {
		for attempt := 0; attempt <= c.retries; attempt++ {
			if err := c.reserveCompletion(); err != nil {
				if len(allErrors) == 0 {
					return "", err
				}
				return "", errors.Join(append(allErrors, err)...)
			}
			response, err := c.client.complete(ctx, model, request)
			if err == nil {
				return response, nil
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			allErrors = append(allErrors, fmt.Errorf("model %s attempt %d: %w", model, attempt+1, err))
			if !retryable(err) || attempt == c.retries {
				break
			}
			delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			if apiErr := (*APIError)(nil); errors.As(err, &apiErr) && apiErr.RetryAfter > delay {
				delay = apiErr.RetryAfter
			}
			c.logger.Warn("AI request failed; retrying", "model", model, "attempt", attempt+1, "delay", delay, "error", err)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	return "", errors.Join(allErrors...)
}

func (c *fallbackClient) reserveCompletion() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxCompletions < 1 || c.completions >= c.maxCompletions {
		return fmt.Errorf("AI_MAX_COMPLETIONS=%d exhausted; the next provider call was not sent. Increase AI_MAX_COMPLETIONS or reduce review batches, deep verification, JSON repair, fallback models, or AI_MAX_RETRIES", c.maxCompletions)
	}
	c.completions++
	return nil
}

type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("AI endpoint returned HTTP %d", e.Status)
}

func retryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusRequestTimeout || apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	return true
}

func retryAfter(response *http.Response) time.Duration {
	raw := response.Header.Get("Retry-After")
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		return time.Until(at)
	}
	return 0
}
