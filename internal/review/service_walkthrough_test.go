package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

func TestUpsertWalkthroughRecoversLegacyHeadingWithoutCreate(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		notes: []gitlab.Note{{ID: 41, Body: walkthroughHeading + "\n\nprevious review"}},
	})
	if stats.creates != 0 {
		t.Fatalf("CreateNote calls = %d, want 0", stats.creates)
	}
	if stats.lists == 0 {
		t.Fatal("expected notes list for cache-miss recovery")
	}
	if !containsInt64(stats.updateIDs, 41) {
		t.Fatalf("UpdateNote ids = %v, want 41", stats.updateIDs)
	}
	if stats.state.WalkthroughID != 41 {
		t.Fatalf("persisted WalkthroughID = %d, want 41", stats.state.WalkthroughID)
	}
	if len(stats.updateBodies) == 0 || !strings.Contains(stats.updateBodies[0], walkthroughMarker) {
		t.Fatalf("progress body missing marker: %v", stats.updateBodies)
	}
}

func TestUpsertWalkthroughRecoversMarkerWithoutCreate(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		notes: []gitlab.Note{
			{ID: 10, Body: walkthroughHeading + "\nolder"},
			{ID: 8, Body: "human comment"},
			{ID: 55, Body: "prior " + walkthroughMarker},
		},
	})
	if stats.creates != 0 {
		t.Fatalf("CreateNote calls = %d, want 0", stats.creates)
	}
	if !containsInt64(stats.updateIDs, 55) {
		t.Fatalf("UpdateNote ids = %v, want latest owned 55", stats.updateIDs)
	}
	if containsInt64(stats.updateIDs, 10) {
		t.Fatalf("updated older walkthrough: %v", stats.updateIDs)
	}
	if stats.state.WalkthroughID != 55 {
		t.Fatalf("persisted WalkthroughID = %d, want 55", stats.state.WalkthroughID)
	}
}

func TestUpsertWalkthroughCreatesWhenNoOwnedNote(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		notes: []gitlab.Note{{ID: 3, Body: "please take a look"}},
	})
	if stats.creates != 1 {
		t.Fatalf("CreateNote calls = %d, want 1", stats.creates)
	}
	if stats.lists == 0 {
		t.Fatal("expected notes list before create")
	}
	if stats.state.WalkthroughID != 99 {
		t.Fatalf("persisted WalkthroughID = %d, want 99", stats.state.WalkthroughID)
	}
	if len(stats.createBodies) == 0 || !strings.Contains(stats.createBodies[0], walkthroughMarker) {
		t.Fatalf("created body missing marker: %v", stats.createBodies)
	}
}

func TestUpsertWalkthroughCreatesWhenListFails(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		notesStatus: http.StatusInternalServerError,
	})
	if stats.creates != 1 {
		t.Fatalf("CreateNote calls = %d, want 1 after list failure", stats.creates)
	}
	if stats.err != nil {
		t.Fatalf("list failure must not fail the review when create succeeds: %v", stats.err)
	}
	if stats.state.WalkthroughID != 99 {
		t.Fatalf("persisted WalkthroughID = %d, want 99", stats.state.WalkthroughID)
	}
}

func TestUpsertWalkthroughPreflightFailsWhenListAndCreateFail(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		notesStatus:  http.StatusInternalServerError,
		createStatus: http.StatusForbidden,
	})
	if stats.err == nil {
		t.Fatal("expected preflight failure when create also fails")
	}
	if stats.creates != 1 {
		t.Fatalf("CreateNote calls = %d, want 1", stats.creates)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 after write preflight failure", stats.aiCalls)
	}
}

func TestDryRunDoesNotListOrWriteWalkthrough(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		dryRun:     true,
		withDiff:   true,
		notes:      []gitlab.Note{{ID: 41, Body: walkthroughHeading}},
		aiResponse: sampleWalkthroughAIJSON(),
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.lists != 0 {
		t.Fatalf("dry-run listed notes %d times", stats.lists)
	}
	if stats.creates != 0 || len(stats.updateIDs) != 0 {
		t.Fatalf("dry-run wrote notes creates=%d updates=%v", stats.creates, stats.updateIDs)
	}
	if stats.aiCalls == 0 {
		t.Fatal("dry-run must still call the model when a diff exists")
	}
	if stats.state.WalkthroughID != 0 {
		t.Fatalf("dry-run persisted WalkthroughID = %d", stats.state.WalkthroughID)
	}
}

func TestSkipAlreadyReviewedDoesNotListNotes(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		lastReviewedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		notes:           []gitlab.Note{{ID: 41, Body: walkthroughHeading}},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.lists != 0 {
		t.Fatalf("SHA skip listed notes %d times", stats.lists)
	}
	if stats.creates != 0 || len(stats.updateIDs) != 0 {
		t.Fatalf("SHA skip wrote notes creates=%d updates=%v", stats.creates, stats.updateIDs)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("SHA skip called AI %d times", stats.aiCalls)
	}
}

func TestFinalWalkthroughReusesRecoveredNote(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		withDiff:   true,
		notes:      []gitlab.Note{{ID: 77, Body: "<!-- codereview-walkthrough -->"}},
		aiResponse: sampleWalkthroughAIJSON(),
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateNote calls = %d, want 0", stats.creates)
	}
	if !containsInt64(stats.updateIDs, 77) {
		t.Fatalf("UpdateNote ids = %v, want 77", stats.updateIDs)
	}
	final := stats.updateBodies[len(stats.updateBodies)-1]
	if !strings.Contains(final, "Adds a helper function") || !strings.Contains(final, walkthroughMarker) {
		t.Fatalf("final walkthrough body = %s", final)
	}
}

func TestStatePersistFailureDoesNotClobberPublishedWalkthrough(t *testing.T) {
	stateDir := t.TempDir()
	storePath := filepath.Join(stateDir, "state.json")
	first := runWalkthroughReview(t, walkthroughReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		storePath:  storePath,
	})
	if first.err != nil {
		t.Fatal(first.err)
	}
	if err := os.Chmod(stateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })

	second := runWalkthroughReview(t, walkthroughReviewOpts{
		withDiff:   true,
		aiResponse: sampleWalkthroughAIJSON(),
		storePath:  storePath,
		force:      true,
	})
	if second.err == nil {
		t.Fatal("expected state persist failure")
	}
	if len(second.updateBodies) == 0 {
		t.Fatal("expected walkthrough note updates")
	}
	final := second.updateBodies[len(second.updateBodies)-1]
	if strings.Contains(final, "The review failed") {
		t.Fatalf("published walkthrough was overwritten after persist failure: %s", final)
	}
	if !strings.Contains(final, "Ready to merge") && !strings.Contains(final, "Needs changes") {
		t.Fatalf("final walkthrough missing verdict: %s", final)
	}
	if !strings.Contains(final, "Adds a helper function") {
		t.Fatalf("final walkthrough missing summary: %s", final)
	}
}

func TestFailedReviewReplacesProgressWalkthrough(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		withDiff: true,
		aiErr:    errors.New("provider down"),
	})
	if stats.err == nil {
		t.Fatal("expected provider failure")
	}
	if len(stats.updateBodies) == 0 && len(stats.createBodies) == 0 {
		t.Fatal("expected a walkthrough note for the failure")
	}
	body := ""
	if len(stats.updateBodies) > 0 {
		body = stats.updateBodies[len(stats.updateBodies)-1]
	} else {
		body = stats.createBodies[len(stats.createBodies)-1]
	}
	if !strings.Contains(body, "The review failed") {
		t.Fatalf("progress note was not replaced with failure: %s", body)
	}
}

func TestCachedWalkthroughIDDoesNotListNotes(t *testing.T) {
	stats := runWalkthroughReview(t, walkthroughReviewOpts{
		walkthroughID: 88,
		notes:         []gitlab.Note{{ID: 41, Body: walkthroughHeading}},
	})
	if stats.err != nil {
		t.Fatal(stats.err)
	}
	if stats.lists != 0 {
		t.Fatalf("cached id listed notes %d times", stats.lists)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateNote calls = %d, want 0", stats.creates)
	}
	if !containsInt64(stats.updateIDs, 88) {
		t.Fatalf("UpdateNote ids = %v, want cached 88", stats.updateIDs)
	}
}

type walkthroughReviewOpts struct {
	notes           []gitlab.Note
	notesStatus     int
	createStatus    int
	dryRun          bool
	withDiff        bool
	force           bool
	walkthroughID   int64
	lastReviewedSHA string
	storePath       string
	aiResponse      string
	aiErr           error
}

type walkthroughReviewStats struct {
	err          error
	lists        int
	creates      int
	aiCalls      int
	updateIDs    []int64
	updateBodies []string
	createBodies []string
	state        state.MRState
}

func runWalkthroughReview(t *testing.T, opts walkthroughReviewOpts) walkthroughReviewStats {
	t.Helper()
	var mu sync.Mutex
	var stats walkthroughReviewStats
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
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
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/diffs"):
			if !opts.withDiff {
				writeJSON(response, []map[string]any{})
				return
			}
			writeJSON(response, []map[string]any{{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n",
			}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/discussions"):
			writeJSON(response, []gitlab.Discussion{})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			mu.Lock()
			stats.lists++
			mu.Unlock()
			if opts.notesStatus != 0 && opts.notesStatus != http.StatusOK {
				http.Error(response, `{"message":"boom"}`, opts.notesStatus)
				return
			}
			notes := opts.notes
			if notes == nil {
				notes = []gitlab.Note{}
			}
			writeJSON(response, notes)
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/discussions/") && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, map[string]any{"id": 3, "body": "reply"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/discussions/"):
			writeJSON(response, map[string]any{"id": "resolved"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/notes"):
			body := readNoteBody(request)
			mu.Lock()
			stats.creates++
			stats.createBodies = append(stats.createBodies, body)
			mu.Unlock()
			if opts.createStatus != 0 && opts.createStatus != http.StatusOK {
				http.Error(response, `{"message":"forbidden"}`, opts.createStatus)
				return
			}
			writeJSON(response, map[string]any{"id": 99, "body": "created"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			body := readNoteBody(request)
			id, _ := strconv.ParseInt(request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:], 10, 64)
			mu.Lock()
			stats.updateIDs = append(stats.updateIDs, id)
			stats.updateBodies = append(stats.updateBodies, body)
			mu.Unlock()
			writeJSON(response, map[string]any{"id": id, "body": "updated"})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	instructionPath := filepath.Join(t.TempDir(), "INSTRUCTION.md")
	if err := os.WriteFile(instructionPath, []byte("Review for correctness."), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := opts.storePath
	if storePath == "" {
		storePath = filepath.Join(t.TempDir(), "state.json")
	}
	store, err := state.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if opts.walkthroughID != 0 || opts.lastReviewedSHA != "" {
		if err := store.Update(12, 34, func(value *state.MRState) {
			value.WalkthroughID = opts.walkthroughID
			value.LastReviewedSHA = opts.lastReviewedSHA
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		Target: config.TargetConfig{DryRun: opts.dryRun},
		Review: config.ReviewConfig{
			DefaultMode:       "standard",
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
	ai := countingAI{response: opts.aiResponse, err: opts.aiErr, calls: &stats.aiCalls, mu: &mu}
	svc := NewService(
		cfg,
		gitlab.NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0),
		ai,
		instructions.Loader{DefaultPath: instructionPath},
		store,
		logx.New(io.Discard, "error"),
	)
	stats.err = svc.Process(context.Background(), Job{
		ProjectID: 12, MergeRequest: 34, Full: true, Force: opts.force, Mode: "standard", Reason: "test",
	})
	stats.state = store.Get(12, 34)
	return stats
}

type countingAI struct {
	response string
	err      error
	calls    *int
	mu       *sync.Mutex
}

func (s countingAI) Complete(_ context.Context, _ ai.Request) (string, error) {
	s.mu.Lock()
	*s.calls++
	s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	return s.response, nil
}

func sampleWalkthroughAIJSON() string {
	payload, err := json.Marshal(AIReview{
		Summary:     "Adds a helper function.",
		Walkthrough: []WalkthroughItem{{Path: "main.go", Summary: "adds helper", Risk: "low"}},
		Findings: []Finding{{
			Path: "main.go", Line: 3, Severity: "low", Category: "correctness",
			Title: "Empty helper", Body: "leak does nothing.", Confidence: 0.95,
		}},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func readNoteBody(request *http.Request) string {
	raw, _ := io.ReadAll(request.Body)
	var payload struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return string(raw)
	}
	return payload.Body
}

func containsInt64(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
