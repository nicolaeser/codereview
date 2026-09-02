package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nicolaeser/codereview/internal/review"
)

func TestOneShotEntrypointHasNoHTTPServer(t *testing.T) {
	// Guard the product model: CI one-shot binary, not a long-running webhook listener.
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, banned := range []string{
		"ListenAndServe",
		"http.Server",
		"internal/webhook",
		"net/http/pprof",
	} {
		if strings.Contains(text, banned) {
			t.Fatalf("main.go must not contain %q for CI one-shot mode", banned)
		}
	}
	if !strings.Contains(text, "service.Process") {
		t.Fatal("main.go must drive the real review.Service.Process path")
	}
}

func TestRunTodosDryRunExitsZeroWithoutMarking(t *testing.T) {
	var marked int
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/todos") {
			writeJSON(response, []map[string]any{{
				"id": 9, "action_name": "mentioned", "target_type": "MergeRequest", "body": "@codereview please review",
				"target_url": "https://gitlab.example.test/group/proj/-/merge_requests/34",
				"project":    map[string]any{"id": 12},
				"target":     map[string]any{"iid": 34, "project_id": 12},
			}})
			return
		}
		if strings.Contains(request.URL.Path, "/mark_as_done") {
			marked++
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(gitlabServer.Close)
	setValidAIEnv(t)
	clearCIEnv(t)
	t.Setenv("GITLAB_API_URL", gitlabServer.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("INSTRUCTION_PATH", writeInstruction(t))
	t.Setenv("STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	t.Setenv("BOT_MENTION", "codereview")
	code := run(context.Background(), []string{"todos", "--dry-run"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if marked != 0 {
		t.Fatalf("dry-run marked %d todos", marked)
	}
}

func TestRunLoginWithoutProviderExitsUsage(t *testing.T) {
	code := run(context.Background(), []string{"login"})
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
}

func TestRunLoginGrokHelpDoesNotStartDeviceCode(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("CODEREVIEW_AUTH_PATH", authPath)
	for _, args := range [][]string{
		{"login", "grok", "-h"},
		{"login", "grok", "--help"},
		{"login", "-h"},
	} {
		code := run(context.Background(), args)
		if code != exitOK {
			t.Fatalf("run(%v) = %d, want %d", args, code, exitOK)
		}
		if _, err := os.Stat(authPath); err == nil {
			t.Fatalf("help started login and wrote %s", authPath)
		}
	}
}

func TestRunMissingConfigExitsNonZero(t *testing.T) {
	clearCIEnv(t)
	t.Setenv("GITLAB_API_URL", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "")
	code := run(context.Background(), nil)
	if code == exitOK {
		t.Fatalf("expected config failure exit, got %d", code)
	}
}

func TestRunMissingTargetExitsNonZero(t *testing.T) {
	setValidAIEnv(t)
	clearCIEnv(t)
	t.Setenv("GITLAB_API_URL", "https://gitlab.example.test/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	code := run(context.Background(), nil)
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
}

func TestRunSuccessfulCIReviewExitsZero(t *testing.T) {
	notes := &sync.Map{}
	gitlabServer, aiServer := startFakeServers(t, notes, sampleAIReviewJSON(false))
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", gitlabServer.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", aiServer.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("POST_INLINE_COMMENTS", "false")
	t.Setenv("POST_PROGRESS_COMMENT", "false")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "false")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")

	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if _, ok := notes.Load("walkthrough"); !ok {
		t.Fatal("expected walkthrough note to be published through the real GitLab client path")
	}
}

func TestRunBlockingFindingsExitsTwo(t *testing.T) {
	notes := &sync.Map{}
	gitlabServer, aiServer := startFakeServers(t, notes, sampleAIReviewJSON(true))
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", gitlabServer.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", aiServer.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("BLOCK_ON_FINDINGS", "true")
	t.Setenv("BLOCKING_SEVERITIES", "critical,high")
	t.Setenv("BLOCKING_REVIEW_MODES", "standard,deep,security")
	t.Setenv("POST_INLINE_COMMENTS", "false")
	t.Setenv("POST_PROGRESS_COMMENT", "false")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "false")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")
	t.Setenv("MINIMUM_CONFIDENCE", "0.1")

	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitBlocked {
		t.Fatalf("exit code = %d, want %d", code, exitBlocked)
	}
}

func TestRunDryRunSkipsGitLabWritesAndExitsZero(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{aiBody: sampleAIReviewJSON(true), traffic: traffic})
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", servers.gitlab.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", servers.ai.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("REVIEW_DRY_RUN", "false")
	t.Setenv("BLOCK_ON_FINDINGS", "true")
	t.Setenv("BLOCKING_SEVERITIES", "critical,high")
	t.Setenv("POST_INLINE_COMMENTS", "true")
	t.Setenv("POST_PROGRESS_COMMENT", "true")
	t.Setenv("POST_WALKTHROUGH", "true")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "true")
	t.Setenv("GITLAB_COMMIT_STATUS_ENABLED", "true")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")
	t.Setenv("MINIMUM_CONFIDENCE", "0.1")

	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full", "--dry-run"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if writes := traffic.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("dry-run issued GitLab writes: %v", writes)
	}
	if traffic.noteListsSnapshot() != 0 {
		t.Fatalf("dry-run listed merge request notes: %d", traffic.noteListsSnapshot())
	}
	if traffic.aiCallsSnapshot() == 0 {
		t.Fatal("dry-run must still call the model")
	}
	if raw, err := os.ReadFile(statePath); err == nil && strings.Contains(string(raw), "last_reviewed_sha") {
		t.Fatalf("dry-run persisted LastReviewedSHA: %s", raw)
	}
}

func TestRunMissingRepoPolicyProceeds(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{aiBody: sampleAIReviewJSON(false), traffic: traffic})
	setInlineReviewEnv(t, servers)
	t.Setenv("POST_INLINE_COMMENTS", "false")
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.aiCallsSnapshot() == 0 {
		t.Fatal("missing .codereview.yml must still call the model")
	}
}

func TestRunValidRepoPolicyIgnorePaths(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:  sampleAIReviewJSON(false),
		traffic: traffic,
		repoFiles: map[string]string{
			".codereview.yml": "review:\n  ignore_paths: [\"main.go\"]\n",
		},
	})
	setInlineReviewEnv(t, servers)
	t.Setenv("POST_INLINE_COMMENTS", "false")
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.aiCallsSnapshot() != 0 {
		t.Fatalf("AI requests = %d, want 0 when yaml ignore_paths drops the diff", traffic.aiCallsSnapshot())
	}
}

func TestRunMalformedRepoPolicyExitsOneWithZeroAI(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:  sampleAIReviewJSON(false),
		traffic: traffic,
		repoFiles: map[string]string{
			".codereview.yml": "review: [",
		},
	})
	setInlineReviewEnv(t, servers)
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if traffic.aiCallsSnapshot() != 0 {
		t.Fatalf("AI requests = %d, want 0 after malformed .codereview.yml", traffic.aiCallsSnapshot())
	}
}

func TestRunForbiddenOnlyRepoPolicyKeysIgnored(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:  sampleAIReviewJSON(false),
		traffic: traffic,
		repoFiles: map[string]string{
			".codereview.yml": "ai:\n  api_key: stolen\n  base_url: https://evil.example.test\ngitlab_token: stolen\nreview:\n  drafts: true\n",
		},
	})
	setInlineReviewEnv(t, servers)
	t.Setenv("POST_INLINE_COMMENTS", "false")
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.aiCallsSnapshot() == 0 {
		t.Fatal("forbidden extra keys must be ignored and the review should proceed")
	}
}

func TestRunWritePreflightFailureSkipsAI(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{aiBody: sampleAIReviewJSON(false), traffic: traffic, notesStatus: http.StatusForbidden})
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", servers.gitlab.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", servers.ai.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("POST_INLINE_COMMENTS", "false")
	t.Setenv("POST_PROGRESS_COMMENT", "false")
	t.Setenv("POST_WALKTHROUGH", "true")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "false")
	t.Setenv("GITLAB_COMMIT_STATUS_ENABLED", "false")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")

	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitFailure {
		t.Fatalf("exit code = %d, want %d", code, exitFailure)
	}
	if traffic.aiCallsSnapshot() != 0 {
		t.Fatalf("AI requests = %d, want 0 after write preflight failure", traffic.aiCallsSnapshot())
	}
}

func TestRunDescriptionUpdateFailureStillPublishes(t *testing.T) {
	notes := &sync.Map{}
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		notes:             notes,
		aiBody:            sampleAIReviewJSON(false),
		traffic:           traffic,
		descriptionStatus: http.StatusForbidden,
	})
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", servers.gitlab.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", servers.ai.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("POST_INLINE_COMMENTS", "true")
	t.Setenv("POST_PROGRESS_COMMENT", "true")
	t.Setenv("POST_WALKTHROUGH", "true")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "true")
	t.Setenv("GITLAB_COMMIT_STATUS_ENABLED", "true")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")
	t.Setenv("MINIMUM_CONFIDENCE", "0.1")

	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	walkthrough, ok := notes.Load("walkthrough")
	if !ok {
		t.Fatal("expected walkthrough note to be published after a description-update failure")
	}
	body := walkthrough.(string)
	if !strings.Contains(body, "Adds a helper function") {
		t.Fatalf("walkthrough missing review summary: %s", body)
	}
	if !strings.Contains(body, "description summary could not be updated") {
		t.Fatalf("walkthrough missing description-update notice: %s", body)
	}
	if _, ok := notes.Load("discussion"); !ok {
		t.Fatal("expected inline discussion to be published after a description-update failure")
	}
	status, ok := notes.Load("status")
	if !ok {
		t.Fatal("expected commit status to be published after a description-update failure")
	}
	if !strings.Contains(status.(string), `"state":"success"`) && !strings.Contains(status.(string), `"state": "success"`) {
		t.Fatalf("commit status = %s, want success", status)
	}
	writes := traffic.writesSnapshot()
	sawDescriptionPUT := false
	for _, write := range writes {
		if strings.HasPrefix(write, "PUT ") && strings.HasSuffix(write, "/merge_requests/34") {
			sawDescriptionPUT = true
			break
		}
	}
	if !sawDescriptionPUT {
		t.Fatalf("expected a failed description PUT, writes = %v", writes)
	}
}

func TestRunSkipsDuplicateUnresolvedInlineDiscussion(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:      sampleAIReviewJSON(false),
		traffic:     traffic,
		discussions: []map[string]any{unresolvedFindingDiscussion("abc", sampleFinding())},
	})
	setInlineReviewEnv(t, servers)
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full", "--force"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.discussionCreates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", traffic.discussionCreates)
	}
	if traffic.discussionResolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", traffic.discussionResolves)
	}
}

func TestRunResolvesStaleInlineWhenLineLeavesDiff(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:      sampleAIReviewJSON(false),
		traffic:     traffic,
		mrDiff:      "@@ -1,3 +1,4 @@\n package main\n+func other() {}\n \n func main() {}\n",
		discussions: []map[string]any{unresolvedFindingDiscussion("stale", sampleFinding())},
	})
	setInlineReviewEnv(t, servers)
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.discussionResolves != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1", traffic.discussionResolves)
	}
	if traffic.discussionCreates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", traffic.discussionCreates)
	}
}

func TestRunPostsInlineWhenListDiscussionsFails(t *testing.T) {
	traffic := &fakeTraffic{}
	servers := startReviewServers(t, reviewServerOpts{
		aiBody:            sampleAIReviewJSON(false),
		traffic:           traffic,
		discussionsStatus: http.StatusInternalServerError,
	})
	setInlineReviewEnv(t, servers)
	code := run(context.Background(), []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	if traffic.discussionCreates != 1 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 1 after list failure", traffic.discussionCreates)
	}
}

// Blocking reviews must not cache the head; a STATE_PATH retry would skip and exit 0.
func TestRunBlockingFindingsRetryWithSameStateStillExitsTwo(t *testing.T) {
	notes := &sync.Map{}
	gitlabServer, aiServer := startFakeServers(t, notes, sampleAIReviewJSON(true))
	setValidAIEnv(t)
	clearCIEnv(t)
	instructionPath := writeInstruction(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	t.Setenv("GITLAB_API_URL", gitlabServer.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", aiServer.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", instructionPath)
	t.Setenv("STATE_PATH", statePath)
	t.Setenv("BLOCK_ON_FINDINGS", "true")
	t.Setenv("BLOCKING_SEVERITIES", "critical,high")
	t.Setenv("BLOCKING_REVIEW_MODES", "standard,deep,security")
	t.Setenv("POST_INLINE_COMMENTS", "false")
	t.Setenv("POST_PROGRESS_COMMENT", "false")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "false")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")
	t.Setenv("MINIMUM_CONFIDENCE", "0.1")

	args := []string{"--project", "12", "--mr", "34", "--mode", "standard", "--full"}
	if code := run(context.Background(), args); code != exitBlocked {
		t.Fatalf("first run exit = %d, want %d", code, exitBlocked)
	}
	// Same STATE_PATH, no --force: must re-evaluate and still fail the gate.
	if code := run(context.Background(), args); code != exitBlocked {
		t.Fatalf("retry with cached state exit = %d, want %d (must not skip as already-reviewed)", code, exitBlocked)
	}
}

func startFakeServers(t *testing.T, notes *sync.Map, aiBody string) (*httptest.Server, *httptest.Server) {
	t.Helper()
	servers := startReviewServers(t, reviewServerOpts{notes: notes, aiBody: aiBody})
	return servers.gitlab, servers.ai
}

type fakeTraffic struct {
	mu                 sync.Mutex
	writes             []string
	aiCalls            int
	noteLists          int
	discussionCreates  int
	discussionResolves int
}

func (f *fakeTraffic) addWrite(method, path string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, method+" "+path)
	if method == http.MethodPost && strings.HasSuffix(path, "/discussions") {
		f.discussionCreates++
	}
	if method == http.MethodPut && strings.Contains(path, "/discussions/") {
		f.discussionResolves++
	}
}

func (f *fakeTraffic) addAI() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aiCalls++
}

func (f *fakeTraffic) addNoteList() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noteLists++
}

func (f *fakeTraffic) noteListsSnapshot() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.noteLists
}

func (f *fakeTraffic) writesSnapshot() []string {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.writes))
	copy(out, f.writes)
	return out
}

func (f *fakeTraffic) aiCallsSnapshot() int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.aiCalls
}

type reviewServerOpts struct {
	notes             *sync.Map
	aiBody            string
	traffic           *fakeTraffic
	notesStatus       int
	descriptionStatus int
	discussions       []map[string]any
	discussionsStatus int
	mrDiff            string
	repoFiles         map[string]string
}

type reviewServers struct {
	gitlab *httptest.Server
	ai     *httptest.Server
}

func startReviewServers(t *testing.T, opts reviewServerOpts) reviewServers {
	t.Helper()
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost || request.Method == http.MethodPut {
			opts.traffic.addWrite(request.Method, request.URL.Path)
		}
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
				"author": map[string]any{"id": 1, "username": "dev"},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/files/"):
			name := repoPolicyNameFromPath(request.URL.Path)
			body, ok := opts.repoFiles[name]
			if !ok {
				http.NotFound(response, request)
				return
			}
			_, _ = response.Write([]byte(body))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/diffs"):
			diff := opts.mrDiff
			if diff == "" {
				diff = "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n"
			}
			writeJSON(response, []map[string]any{{
				"old_path": "main.go", "new_path": "main.go",
				"diff": diff,
			}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/discussions"):
			if opts.discussionsStatus != 0 && opts.discussionsStatus != http.StatusOK {
				http.Error(response, `{"message":"boom"}`, opts.discussionsStatus)
				return
			}
			list := opts.discussions
			if list == nil {
				list = []map[string]any{}
			}
			writeJSON(response, list)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			opts.traffic.addNoteList()
			writeJSON(response, []map[string]any{})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/versions"):
			writeJSON(response, []map[string]any{{
				"id":               1,
				"head_commit_sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"base_commit_sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"start_commit_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			}})
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/discussions/") && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, map[string]any{"id": 3, "body": "reply"})
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/notes"):
			if opts.notesStatus != 0 && opts.notesStatus != http.StatusOK {
				http.Error(response, `{"message":"forbidden"}`, opts.notesStatus)
				return
			}
			body, _ := io.ReadAll(request.Body)
			if opts.notes != nil {
				opts.notes.Store("walkthrough", string(body))
			}
			writeJSON(response, map[string]any{"id": 99, "body": string(body)})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			body, _ := io.ReadAll(request.Body)
			if opts.notes != nil {
				opts.notes.Store("walkthrough", string(body))
			}
			writeJSON(response, map[string]any{"id": 99, "body": string(body)})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/discussions"):
			if opts.notes != nil {
				opts.notes.Store("discussion", true)
			}
			writeJSON(response, map[string]any{"id": "d1", "notes": []map[string]any{{"id": 1}}})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/merge_requests/34/discussions/"):
			writeJSON(response, map[string]any{"id": "resolved"})
		case request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			if opts.descriptionStatus != 0 && opts.descriptionStatus != http.StatusOK {
				http.Error(response, `{"message":"forbidden"}`, opts.descriptionStatus)
				return
			}
			writeJSON(response, map[string]any{"id": 340, "iid": 34})
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/statuses/"):
			body, _ := io.ReadAll(request.Body)
			if opts.notes != nil {
				opts.notes.Store("status", string(body))
			}
			writeJSON(response, map[string]any{"id": 1})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(gitlabServer.Close)

	aiServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		opts.traffic.addAI()
		if !strings.Contains(request.URL.Path, "chat/completions") {
			http.NotFound(response, request)
			return
		}
		writeJSON(response, map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": opts.aiBody},
			}},
		})
	}))
	t.Cleanup(aiServer.Close)
	return reviewServers{gitlab: gitlabServer, ai: aiServer}
}

func sampleFinding() review.Finding {
	return review.Finding{Path: "main.go", Line: 3, Title: "Empty helper"}
}

func unresolvedFindingDiscussion(id string, finding review.Finding) map[string]any {
	return map[string]any{
		"id":              id,
		"individual_note": false,
		"notes": []map[string]any{{
			"id":         1,
			"body":       "prior <!-- codereview-finding:" + review.FindingID(finding) + " -->",
			"resolvable": true,
			"resolved":   false,
			"author":     map[string]any{"username": "codereview"},
		}},
	}
}

func setInlineReviewEnv(t *testing.T, servers reviewServers) {
	t.Helper()
	setValidAIEnv(t)
	clearCIEnv(t)
	t.Setenv("GITLAB_API_URL", servers.gitlab.URL+"/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("AI_BASE_URL", servers.ai.URL+"/v1")
	t.Setenv("AI_AUTH_MODE", "none")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("INSTRUCTION_PATH", writeInstruction(t))
	t.Setenv("STATE_PATH", filepath.Join(t.TempDir(), "state.json"))
	t.Setenv("POST_INLINE_COMMENTS", "true")
	t.Setenv("POST_PROGRESS_COMMENT", "false")
	t.Setenv("POST_WALKTHROUGH", "true")
	t.Setenv("SUMMARY_IN_DESCRIPTION", "false")
	t.Setenv("GITLAB_COMMIT_STATUS_ENABLED", "false")
	t.Setenv("INCLUDE_REPOSITORY_TREE", "false")
	t.Setenv("INCLUDE_CHANGED_FILE_CONTENT", "false")
	t.Setenv("INCLUDE_RELATED_FILES", "false")
	t.Setenv("INCLUDE_REPOSITORY_GUIDELINES", "false")
	t.Setenv("INCLUDE_LINKED_ISSUES", "false")
	t.Setenv("DEEP_REVIEW_VERIFICATION", "false")
	t.Setenv("MINIMUM_CONFIDENCE", "0.1")
}

func sampleAIReviewJSON(blocking bool) string {
	severity := "low"
	if blocking {
		severity = "critical"
	}
	return `{
  "summary": "Adds a helper function.",
  "walkthrough": [{"path":"main.go","summary":"adds leak helper","risk":"low"}],
  "issue_assessments": [],
  "findings": [{
    "path": "main.go",
    "line": 3,
    "severity": "` + severity + `",
    "category": "correctness",
    "title": "Empty helper",
    "body": "leak does nothing and may indicate incomplete work.",
    "suggestion": "",
    "confidence": 0.95
  }]
}`
}

func writeInstruction(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "INSTRUCTION.md")
	if err := os.WriteFile(path, []byte("Review for correctness and security."), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setValidAIEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MODEL", "test-model")
	t.Setenv("AI_API_KEY", "key")
	t.Setenv("AI_JSON_MODE", "prompt")
	t.Setenv("AI_JSON_REPAIR", "false")
	t.Setenv("OPENAI_CHAT_COMPLETIONS_PATH", "/chat/completions")
	t.Setenv("LOG_LEVEL", "error")
}

func clearCIEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CI_PROJECT_ID", "CI_MERGE_REQUEST_IID", "PROJECT_ID", "MERGE_REQUEST_IID",
		"CI_API_V4_URL", "CI_JOB_TOKEN", "CI_PIPELINE_ID", "CI_JOB_ID",
		"REVIEW_MODE", "REVIEW_FULL", "REVIEW_FORCE", "REVIEW_SUMMARY_ONLY", "REVIEW_DRY_RUN",
		"BLOCK_ON_FINDINGS",
	} {
		t.Setenv(key, "")
	}
}

func repoPolicyNameFromPath(urlPath string) string {
	switch {
	case strings.Contains(urlPath, "/files/.codereview.yml/"):
		return ".codereview.yml"
	case strings.Contains(urlPath, "/files/.codereview.yaml/"):
		return ".codereview.yaml"
	default:
		return ""
	}
}

func writeJSON(response http.ResponseWriter, payload any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}
