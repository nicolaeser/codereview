package review

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

func TestTodoMergeRequestFromTargetAndURL(t *testing.T) {
	mrTodo := gitlab.Todo{TargetType: "MergeRequest"}
	mrTodo.Project.ID = 12
	mrTodo.Target.IID = 34
	mrTodo.Target.ProjectID = 12
	project, iid, ok := todoMergeRequest(mrTodo)
	if !ok || project != 12 || iid != 34 {
		t.Fatalf("MR target = %d %d ok=%t", project, iid, ok)
	}
	noteTodo := gitlab.Todo{TargetType: "Note", TargetURL: "https://gitlab.example.test/group/proj/-/merge_requests/9#note_1"}
	noteTodo.Project.ID = 7
	project, iid, ok = todoMergeRequest(noteTodo)
	if !ok || project != 7 || iid != 9 {
		t.Fatalf("note URL = %d %d ok=%t", project, iid, ok)
	}
	if _, _, ok := todoMergeRequest(gitlab.Todo{TargetType: "Issue", TargetURL: "https://gitlab.example.test/group/proj/-/issues/1"}); ok {
		t.Fatal("issue todo must be ignored")
	}
}

func TestTodoMentionsBot(t *testing.T) {
	todo := gitlab.Todo{Body: "please look @codereview", ActionName: "mentioned"}
	if !todoMentionsBot(todo, []string{"codereview", "cr"}) {
		t.Fatal("expected mention match")
	}
	if todoMentionsBot(gitlab.Todo{Body: "no bot here"}, []string{"codereview"}) {
		t.Fatal("unexpected mention")
	}
}

func TestDrainMentionTodosDryRunDoesNotMark(t *testing.T) {
	stats := runTodoDrain(t, todoDrainOpts{dryRun: true})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.marked != 0 || stats.reviewed == 0 {
		t.Fatalf("dry-run marked=%d reviewed=%d", stats.marked, stats.reviewed)
	}
}

func TestDrainMentionTodosBareMentionPostsHelp(t *testing.T) {
	stats := runTodoDrain(t, todoDrainOpts{helpOnly: true})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.reviewed != 1 || stats.marked != 1 {
		t.Fatalf("reviewed=%d marked=%d", stats.reviewed, stats.marked)
	}
	if stats.helpNotes != 1 {
		t.Fatalf("help notes = %d, want 1", stats.helpNotes)
	}
}

func TestDrainMentionTodosMarksDoneAfterReview(t *testing.T) {
	stats := runTodoDrain(t, todoDrainOpts{})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.reviewed != 1 || stats.marked != 1 {
		t.Fatalf("reviewed=%d marked=%d", stats.reviewed, stats.marked)
	}
}

func TestDrainMentionTodosDoesNotMarkOnReviewFailure(t *testing.T) {
	stats := runTodoDrain(t, todoDrainOpts{failReview: true})
	if stats.err == nil {
		t.Fatal("expected review failure")
	}
	if stats.marked != 0 {
		t.Fatalf("marked = %d, want 0", stats.marked)
	}
}

type todoDrainOpts struct {
	dryRun     bool
	failReview bool
	helpOnly   bool
}

type todoDrainStats struct {
	err       error
	reviewed  int
	marked    int32
	helpNotes int32
}

func runTodoDrain(t *testing.T, opts todoDrainOpts) todoDrainStats {
	t.Helper()
	var stats todoDrainStats
	mrTodoBody := "@codereview please review"
	if opts.helpOnly {
		mrTodoBody = "@codereview"
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v4/todos":
			writeJSON(response, []map[string]any{
				{
					"id": 8, "action_name": "mentioned", "target_type": "Issue", "body": "@codereview look",
					"target_url": "https://gitlab.example.test/group/proj/-/issues/1", "project": map[string]any{"id": 12},
				},
				{
					"id": 9, "action_name": "mentioned", "target_type": "MergeRequest", "body": mrTodoBody,
					"target_url": "https://gitlab.example.test/group/proj/-/merge_requests/34",
					"project":    map[string]any{"id": 12},
					"target":     map[string]any{"iid": 34, "project_id": 12},
				},
			})
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/todos/") && strings.HasSuffix(request.URL.Path, "/mark_as_done"):
			atomic.AddInt32(&stats.marked, 1)
			writeJSON(response, map[string]any{"id": 9})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			if opts.failReview {
				http.Error(response, `{"message":"nope"}`, http.StatusInternalServerError)
				return
			}
			writeJSON(response, map[string]any{
				"id": 340, "iid": 34, "project_id": 12, "title": "Add helper", "description": "demo",
				"state": "opened", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"web_url": "https://gitlab.example.test/group/proj/-/merge_requests/34",
				"diff_refs": map[string]string{
					"base_sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"start_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"head_sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				"author": map[string]any{"username": "dev"},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/diffs"):
			writeJSON(response, []map[string]any{{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n",
			}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/discussions"):
			writeJSON(response, []any{})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, []any{})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/notes"):
			raw, _ := io.ReadAll(request.Body)
			if strings.Contains(string(raw), "codereview-help") {
				atomic.AddInt32(&stats.helpNotes, 1)
			}
			writeJSON(response, map[string]any{"id": 99, "body": "walkthrough"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			writeJSON(response, map[string]any{"id": 99, "body": "walkthrough"})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/files/"):
			http.NotFound(response, request)
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
			DefaultMode:        "standard",
			PostSummary:        true,
			PostInline:         false,
			MaxDiffFiles:       10,
			MaxDiffChars:       20000,
			MaxComments:        12,
			MinimumConfidence:  0.1,
			CommentStyle:       "compact",
			Mention:            "codereview",
			MentionAliases:     []string{"codereview", "cr"},
			BlockingSeverities: []string{"critical", "high"},
			TodoMax:            5,
		},
		AI:     config.AIConfig{MaxInputChars: 20000, Provider: "openai"},
		Target: config.TargetConfig{DryRun: opts.dryRun},
	}
	svc := NewService(
		cfg,
		gitlab.NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0),
		stubAI{response: inlineAIReviewJSON(lowFinding())},
		instructions.Loader{DefaultPath: instructionPath},
		store,
		logx.New(io.Discard, "error"),
	)
	result, err := svc.DrainMentionTodos(context.Background())
	stats.err = err
	stats.reviewed = result.Reviewed
	return stats
}
