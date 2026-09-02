package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

func TestRunHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBHOOK_LISTEN", strings.TrimPrefix(srv.URL, "http://"))
	if code := runHealth(); code != 0 {
		t.Fatalf("runHealth() = %d, want 0", code)
	}
	srv.Close()
	if code := runHealth(); code == 0 {
		t.Fatal("runHealth() succeeded against a stopped server")
	}
}

func TestRunRefusesEmptyWebhookSecret(t *testing.T) {
	t.Setenv("GITLAB_API_URL", "https://gitlab.example.test/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MODEL", "test-model")
	t.Setenv("AI_API_KEY", "key")
	t.Setenv("AI_JSON_MODE", "prompt")
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_SIGNING_TOKEN", "")
	t.Setenv("WEBHOOK_LISTEN", "")
	t.Setenv("WEBHOOK_MAX_BODY", "")
	t.Setenv("WEBHOOK_PATH", "")
	t.Setenv("CI_PROJECT_ID", "")
	t.Setenv("CI_MERGE_REQUEST_IID", "")
	code := run(context.Background())
	if code == 0 {
		t.Fatal("empty WEBHOOK_SECRET and WEBHOOK_SIGNING_TOKEN must fail at startup")
	}
}

func TestServiceRunnerSkipDiscussionRequiresID(t *testing.T) {
	runner := serviceRunner{}
	err := runner.SkipDiscussion(context.Background(), 1, 1, "", "@codereview skip", "root")
	if err == nil {
		t.Fatal("empty discussion id must error")
	}
}

func TestServiceRunnerSkipDiscussionRepliesAndResolves(t *testing.T) {
	var replies atomic.Int32
	var resolves atomic.Int32
	var replyBody string
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/discussions/abc/notes"):
			replies.Add(1)
			raw, _ := io.ReadAll(request.Body)
			replyBody = string(raw)
			writeJSON(response, map[string]any{"id": 9, "body": "Skipped after @mention (false positive)."})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/discussions/abc"):
			resolves.Add(1)
			writeJSON(response, map[string]any{"id": "abc", "resolved": true})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(gitlabServer.Close)

	runner := serviceRunner{gitlab: gitlab.New(gitlabServer.URL+"/api/v4", "token", 2*time.Second)}
	if err := runner.SkipDiscussion(context.Background(), 1, 2, "abc", "@codereview skip", "root"); err != nil {
		t.Fatal(err)
	}
	if replies.Load() != 1 {
		t.Fatalf("ReplyToDiscussion calls = %d, want 1", replies.Load())
	}
	if resolves.Load() != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1", resolves.Load())
	}
	if !strings.Contains(replyBody, "codereview-skip") || !strings.Contains(replyBody, "Skipped as a false positive after @mention.") {
		t.Fatalf("reply body = %s", replyBody)
	}
}

func writeJSON(response http.ResponseWriter, payload any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}
