package review

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

type repoPolicyReviewOpts struct {
	draft       bool
	files       map[string]string
	fileStatus  int
	withDiff    bool
	aiResponse  string
	reviewDraft bool
}

type repoPolicyReviewStats struct {
	err     error
	aiCalls int
	fileRef string
	files   []string
}

func TestMissingRepoPolicyFileReviewProceeds(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{withDiff: true, aiResponse: sampleWalkthroughAIJSON()})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls == 0 {
		t.Fatal("missing .codereview.yml must not block the model call")
	}
	if len(stats.files) < 2 {
		t.Fatalf("expected yml then yaml lookup, got %v", stats.files)
	}
	if stats.files[0] != ".codereview.yml" || stats.files[1] != ".codereview.yaml" {
		t.Fatalf("lookup order = %v", stats.files)
	}
	if stats.fileRef != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("policy ref = %q, want reviewed SHA", stats.fileRef)
	}
}

func TestIgnoreFileDropsDiffWithoutYAML(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		files:      map[string]string{".codereviewignore": "# generated\nmain.go\n"},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls != 0 {
		t.Fatalf(".codereviewignore should drop the only diff file before AI, calls=%d files=%v", stats.aiCalls, stats.files)
	}
}

func TestRepoPolicyYAMLFallbackWhenYMLMissing(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		files:      map[string]string{".codereview.yaml": "review:\n  ignore_paths: [\"main.go\"]\n"},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("yaml ignore_paths should drop the only diff file before AI, calls=%d", stats.aiCalls)
	}
}

func TestRepoPolicyYMLPreferredOverYAML(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		files: map[string]string{
			".codereview.yml":  "review:\n  ignore_paths: [\"main.go\"]\n",
			".codereview.yaml": "review: [",
		},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("yml should win over malformed yaml sibling, calls=%d", stats.aiCalls)
	}
}

func TestRepoPolicyValidYAMLSetsIgnorePathsAndDrafts(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		draft:      true,
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		files:      map[string]string{".codereview.yml": "review:\n  drafts: true\n  ignore_paths: [\"vendor/**\"]\n"},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls == 0 {
		t.Fatal("yaml drafts=true should review a draft MR when env is unset")
	}
}

func TestRepoPolicyMalformedYAMLFailsBeforeAI(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		files:      map[string]string{".codereview.yml": "review: ["},
	})
	if stats.err == nil {
		t.Fatal("expected malformed policy to fail the review")
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
}

func TestRepoPolicyFetchErrorFailsClosed(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		fileStatus: http.StatusInternalServerError,
	})
	if stats.err == nil {
		t.Fatal("expected GitLab policy fetch failure")
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
}

func TestDraftSkippedWhenRepoPolicyDoesNotEnableDrafts(t *testing.T) {
	stats := runRepoPolicyReview(t, repoPolicyReviewOpts{
		draft:      true,
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("draft skip should happen after missing policy no-op, calls=%d", stats.aiCalls)
	}
}

func runRepoPolicyReview(t *testing.T, opts repoPolicyReviewOpts) repoPolicyReviewStats {
	t.Helper()
	var mu sync.Mutex
	var stats repoPolicyReviewStats
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			writeJSON(response, map[string]any{
				"id": 340, "iid": 34, "project_id": 12, "title": "Add helper", "description": "demo",
				"state": "opened", "draft": opts.draft, "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"web_url": "https://gitlab.example.test/group/proj/-/merge_requests/34",
				"diff_refs": map[string]string{
					"base_sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"start_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"head_sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				"author": map[string]any{"username": "dev"},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/files/"):
			mu.Lock()
			stats.fileRef = request.URL.Query().Get("ref")
			name := repoPolicyNameFromPath(request.URL.Path)
			stats.files = append(stats.files, name)
			mu.Unlock()
			if opts.fileStatus != 0 && opts.fileStatus != http.StatusOK {
				http.Error(response, `{"message":"boom"}`, opts.fileStatus)
				return
			}
			body, ok := opts.files[name]
			if !ok {
				http.NotFound(response, request)
				return
			}
			_, _ = response.Write([]byte(body))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/diffs"):
			if !opts.withDiff {
				writeJSON(response, []map[string]any{})
				return
			}
			writeJSON(response, []map[string]any{{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n",
			}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, []map[string]any{})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, map[string]any{"id": 99, "body": "created"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			writeJSON(response, map[string]any{"id": 99, "body": "updated"})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	instructionPath := filepath.Join(t.TempDir(), "INSTRUCTION.md")
	if err := os.WriteFile(instructionPath, []byte("Review for correctness."), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Review: config.ReviewConfig{
			DefaultMode:       "standard",
			ReviewDrafts:      opts.reviewDraft,
			PostSummary:       true,
			PostProgress:      true,
			MaxDiffFiles:      10,
			MaxDiffChars:      20000,
			MaxComments:       12,
			MinimumConfidence: 0.1,
			CommentStyle:      "compact",
			Mention:           "codereview",
		},
		AI: config.AIConfig{MaxInputChars: 20000, Provider: "openai"},
	}
	ai := countingAI{response: opts.aiResponse, calls: &stats.aiCalls, mu: &mu}
	svc := NewService(
		cfg,
		gitlab.NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0),
		ai,
		instructions.Loader{DefaultPath: instructionPath},
		store,
		logx.New(io.Discard, "error"),
	)
	stats.err = svc.Process(context.Background(), Job{
		ProjectID: 12, MergeRequest: 34, Full: true, Mode: "standard", Reason: "test",
	})
	return stats
}

func repoPolicyNameFromPath(urlPath string) string {
	switch {
	case strings.Contains(urlPath, "/files/.codereview.yml/"):
		return ".codereview.yml"
	case strings.Contains(urlPath, "/files/.codereview.yaml/"):
		return ".codereview.yaml"
	case strings.Contains(urlPath, "/files/.codereviewignore/"):
		return ".codereviewignore"
	case strings.Contains(urlPath, "/files/.codereview%2Fignore/") || strings.Contains(urlPath, "/files/.codereview/ignore/"):
		return ".codereview/ignore"
	default:
		return urlPath
	}
}
