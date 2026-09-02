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

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

func TestApproveOnCleanCallsApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: true, finding: lowFinding(), user: "coder"})
	if stats.approves != 1 {
		t.Fatalf("approves = %d, want 1", stats.approves)
	}
	if stats.unapproves != 0 {
		t.Fatalf("unapproves = %d, want 0", stats.unapproves)
	}
}

func TestNeedsChangesUnapprovesExistingApproval(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: true, finding: sampleInlineFinding(), alreadyApproved: true, user: "coder"})
	if stats.unapproves != 1 {
		t.Fatalf("unapproves = %d, want 1", stats.unapproves)
	}
	if stats.approves != 0 {
		t.Fatalf("approves = %d, want 0", stats.approves)
	}
}

func TestApproveFlagOffDoesNothing(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: false, finding: lowFinding(), user: "coder"})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("approves=%d unapproves=%d", stats.approves, stats.unapproves)
	}
}

func TestApproveDryRunDoesNotWrite(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: true, dryRun: true, finding: lowFinding(), user: "coder"})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("dry-run wrote approvals: %d %d", stats.approves, stats.unapproves)
	}
}

func TestApproveForbiddenFailsClosed(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: true, finding: lowFinding(), user: "coder", approveStatus: http.StatusForbidden})
	if stats.err == nil {
		t.Fatal("expected approve 403 to fail the review")
	}
	if stats.approves != 1 {
		t.Fatalf("approve attempts = %d, want 1", stats.approves)
	}
}

func TestApproveRejectsProjectBotBeforeModel(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{approve: true, finding: lowFinding(), user: "project_1_bot_abc", bot: true})
	if stats.err == nil {
		t.Fatal("expected bot token to fail closed")
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
	if stats.approves != 0 {
		t.Fatalf("approves = %d, want 0", stats.approves)
	}
}

func TestRequestReviewAddsUserWithoutWipingOthers(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{requestReview: true, finding: lowFinding(), user: "coder", existingReviewer: 99})
	if stats.reviewerPuts != 1 {
		t.Fatalf("reviewer updates = %d, want 1", stats.reviewerPuts)
	}
	if !strings.Contains(stats.reviewerBody, "99") || !strings.Contains(stats.reviewerBody, "42") {
		t.Fatalf("reviewer_ids should keep existing reviewers, body=%s", stats.reviewerBody)
	}
}

func TestDependencyFlagsOffDoNotClassifyOrApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{finding: lowFinding(), user: "coder", depDiff: true})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("approves=%d unapproves=%d", stats.approves, stats.unapproves)
	}
	if strings.Contains(stats.noteBody, "dependency manifests/lockfiles") {
		t.Fatalf("flags off must not classify dependency bumps: %s", stats.noteBody)
	}
	if strings.Contains(stats.aiSystem, "dependency/lockfile bump") {
		t.Fatal("flags off must not extra-prompt")
	}
}

func TestApproveDependencyBumpsApprovesCleanLockfileOnlyMR(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, finding: lowFinding(), user: "coder",
	})
	if stats.approves != 1 {
		t.Fatalf("approves = %d, want 1", stats.approves)
	}
	if stats.unapproves != 0 {
		t.Fatalf("unapproves = %d, want 0", stats.unapproves)
	}
	if !strings.Contains(stats.noteBody, "dependency-only") {
		t.Fatalf("walkthrough should mention dependency-only approve, body=%s", stats.noteBody)
	}
	if strings.Contains(stats.aiSystem, "dependency/lockfile bump") {
		t.Fatal("approve-only must not extra-prompt")
	}
}

func TestApproveDependencyBumpsSkipsWhenSourceFilesChange(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, finding: lowFinding(), user: "coder",
	})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("approves=%d unapproves=%d", stats.approves, stats.unapproves)
	}
	if !strings.Contains(strings.ToLower(stats.noteBody), "auto-approve for dependency bumps") {
		t.Fatalf("expected skip notice, body=%s", stats.noteBody)
	}
}

func TestApproveDependencyBumpsUnapprovesBlockingFinding(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, finding: sampleInlineFinding(), alreadyApproved: true, user: "coder",
	})
	if stats.unapproves != 1 {
		t.Fatalf("unapproves = %d, want 1", stats.unapproves)
	}
	if stats.approves != 0 {
		t.Fatalf("approves = %d, want 0", stats.approves)
	}
}

func TestApproveOnCleanStillApprovesNonDependencyMR(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approve: true, approveDependencyBumps: true, finding: lowFinding(), user: "coder",
	})
	if stats.approves != 1 {
		t.Fatalf("approves = %d, want 1", stats.approves)
	}
}

func TestPolicyFileChangeClearsApproveDependencyBumpsWithoutOperatorEnv(t *testing.T) {
	t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "")
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, extraFiles: []string{".codereview.yml"},
		finding: lowFinding(), user: "coder",
	})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("approves=%d unapproves=%d", stats.approves, stats.unapproves)
	}
	if !strings.Contains(stats.noteBody, "GITLAB_APPROVE_DEPENDENCY_BUMPS") {
		t.Fatalf("expected policy-file auto-approve ignore notice, body=%s", stats.noteBody)
	}
}

func TestApproveDependencyBumpsDryRunDoesNotWrite(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, dryRun: true, finding: lowFinding(), user: "coder",
	})
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("dry-run wrote approvals: %d %d", stats.approves, stats.unapproves)
	}
	if stats.aiCalls == 0 {
		t.Fatal("dry-run must still call the model")
	}
}

func TestApproveDependencyBumpsForbiddenFailsClosed(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, finding: lowFinding(), user: "coder", approveStatus: http.StatusForbidden,
	})
	if stats.err == nil {
		t.Fatal("expected approve 403 to fail the review")
	}
	if stats.approves != 1 {
		t.Fatalf("approve attempts = %d, want 1", stats.approves)
	}
}

func TestApproveDependencyBumpsRejectsProjectBotBeforeModel(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		approveDependencyBumps: true, depDiff: true, finding: lowFinding(), user: "project_1_bot_abc", bot: true,
	})
	if stats.err == nil {
		t.Fatal("expected bot token to fail closed")
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
	if stats.approves != 0 {
		t.Fatalf("approves = %d, want 0", stats.approves)
	}
}

func TestDependencyBumpsEnablesFocusedProtocol(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		dependencyBumps: true, depDiff: true, finding: lowFinding(), user: "coder",
	})
	if stats.approves != 0 {
		t.Fatalf("review-bumps without approve flag must not approve, approves=%d", stats.approves)
	}
	if !strings.Contains(stats.aiSystem, "dependency/lockfile bump") {
		t.Fatalf("expected focused dependency protocol, system=%q", stats.aiSystem)
	}
	if !strings.Contains(stats.noteBody, "only changes dependency manifests/lockfiles") {
		t.Fatalf("expected dependency-only notice, body=%s", stats.noteBody)
	}
}

func TestGitLabWritesEnabledIncludesApproveDependencyBumps(t *testing.T) {
	cfg := config.ReviewConfig{ApproveDependencyBumps: true}
	if !gitlabWritesEnabled(cfg, Job{SummaryOnly: true}, false) {
		t.Fatal("ApproveDependencyBumps must enable GitLab writes")
	}
	s := &Service{cfg: config.Config{Review: config.ReviewConfig{ApproveDependencyBumps: true}}}
	if !s.identityWritesEnabled() {
		t.Fatal("ApproveDependencyBumps must enable identity writes")
	}
	cfg.ApproveDependencyBumps = false
	if gitlabWritesEnabled(cfg, Job{SummaryOnly: true}, false) {
		t.Fatal("writes must stay disabled when approve flags are off")
	}
	cfg.DependencyBumpMode = config.DependencyBumpModeValidate
	if !gitlabWritesEnabled(cfg, Job{SummaryOnly: true}, false) {
		t.Fatal("validate bump mode must enable GitLab writes")
	}
}

type approvalOpts struct {
	approve                bool
	approveDependencyBumps bool
	dependencyBumps        bool
	bumpMode               string
	pushURL                string
	pushSecret             string
	automatic              bool
	reason                 string
	aiJSON                 string
	requestReview          bool
	dryRun                 bool
	depDiff                bool
	extraFiles             []string
	finding                Finding
	user                   string
	bot                    bool
	alreadyApproved        bool
	approveStatus          int
	existingReviewer       int64
}

type approvalStats struct {
	err              error
	approves         int32
	unapproves       int32
	reviewerPuts     int32
	reviewerBody     string
	noteBody         string
	aiSystem         string
	aiCalls          int32
	pushPosts        int32
	pushBody         string
	pushToken        string
	approveFlagAfter bool
}

func lowFinding() Finding {
	f := sampleInlineFinding()
	f.Severity = "low"
	f.Title = "Minor note"
	return f
}

func runApprovalReview(t *testing.T, opts approvalOpts) approvalStats {
	t.Helper()
	var stats approvalStats
	finding := opts.finding
	if opts.depDiff && finding.Path == "main.go" {
		finding.Path = "go.mod"
	}
	pushServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&stats.pushPosts, 1)
		body, _ := io.ReadAll(request.Body)
		stats.pushBody = string(body)
		stats.pushToken = request.Header.Get("X-CodeReview-Token")
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(pushServer.Close)
	if opts.pushURL == "httptest" {
		opts.pushURL = pushServer.URL
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v4/user":
			writeJSON(response, map[string]any{"id": 42, "username": opts.user, "bot": opts.bot})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/approvals"):
			approvedBy := []map[string]any{}
			if opts.alreadyApproved {
				approvedBy = []map[string]any{{"user": map[string]any{"id": 42, "username": opts.user}}}
			}
			writeJSON(response, map[string]any{"approved": opts.alreadyApproved, "approved_by": approvedBy})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/unapprove"):
			atomic.AddInt32(&stats.unapproves, 1)
			writeJSON(response, map[string]any{"id": 1})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/approve"):
			atomic.AddInt32(&stats.approves, 1)
			if opts.approveStatus != 0 && opts.approveStatus != http.StatusOK {
				http.Error(response, `{"message":"forbidden"}`, opts.approveStatus)
				return
			}
			writeJSON(response, map[string]any{"id": 1})
		case request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			atomic.AddInt32(&stats.reviewerPuts, 1)
			body, _ := io.ReadAll(request.Body)
			stats.reviewerBody = string(body)
			writeJSON(response, map[string]any{"iid": 34})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			reviewers := []map[string]any{}
			if opts.existingReviewer != 0 {
				reviewers = []map[string]any{{"id": opts.existingReviewer, "username": "human"}}
			}
			writeJSON(response, map[string]any{
				"id": 340, "iid": 34, "project_id": 12, "title": "Add helper", "description": "demo",
				"state": "opened", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"source_branch": "dependabot/go_modules/foo-1.2.3",
				"target_branch": "main",
				"web_url":       "https://gitlab.example.test/group/proj/-/merge_requests/34",
				"references":    map[string]string{"full": "group/proj!34"},
				"diff_refs": map[string]string{
					"base_sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"start_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"head_sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				"author":    map[string]any{"username": "dev"},
				"reviewers": reviewers,
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/diffs"):
			writeJSON(response, approvalDiffs(opts))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/discussions"):
			writeJSON(response, []any{})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			writeJSON(response, []any{})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/notes"):
			stats.noteBody = readNoteBody(request)
			writeJSON(response, map[string]any{"id": 99, "body": "walkthrough"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			stats.noteBody = readNoteBody(request)
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
			DefaultMode:            "standard",
			PostSummary:            true,
			PostInline:             false,
			ApproveOnClean:         opts.approve,
			ApproveDependencyBumps: opts.approveDependencyBumps,
			DependencyBumps:        opts.dependencyBumps,
			DependencyBumpMode:     opts.bumpMode,
			BumpPushURL:            opts.pushURL,
			BumpPushSecret:         opts.pushSecret,
			RequestReview:          opts.requestReview,
			MaxDiffFiles:           10,
			MaxDiffChars:           20000,
			MaxComments:            12,
			MinimumConfidence:      0.1,
			CommentStyle:           "compact",
			Mention:                "codereview",
			BlockingSeverities:     []string{"critical", "high"},
			BlockingReviewModes:    []string{"standard"},
			TodoMax:                5,
		},
		AI:     config.AIConfig{MaxInputChars: 20000, Provider: "openai"},
		Target: config.TargetConfig{DryRun: opts.dryRun},
	}
	aiResponse := inlineAIReviewJSON(finding)
	if opts.aiJSON != "" {
		aiResponse = opts.aiJSON
	}
	force := !opts.automatic
	reason := opts.reason
	if reason == "" {
		reason = "test"
	}
	svc := NewService(
		cfg,
		gitlab.NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0),
		countingApprovalAI{response: aiResponse, calls: &stats.aiCalls, lastSystem: &stats.aiSystem},
		instructions.Loader{DefaultPath: instructionPath},
		store,
		logx.New(io.Discard, "error"),
	)
	stats.err = svc.Process(context.Background(), Job{
		ProjectID: 12, MergeRequest: 34, Full: true, Force: force, Mode: "standard", Reason: reason,
	})
	stats.approveFlagAfter = svc.cfg.Review.ApproveDependencyBumps
	return stats
}

func agentReportAIJSON() string {
	return `{
  "summary": "Bumps foo to v1.2.3.",
  "walkthrough": [{"path":"go.mod","summary":"require bump","risk":"low"}],
  "findings": [],
  "agent_report": "Prepare the codebase for foo v1.2.3: update constructors that still call OldAPI, add a regression test for the new error type, and leave lockfile regeneration to the external agent.",
  "branch_name": "codereview/adapt-foo-v1.2.3",
  "code_adaptation_required": true
}`
}

func validateCleanAIJSON() string {
	return `{
  "summary": "Lockfile-only bump.",
  "walkthrough": [{"path":"go.mod","summary":"require bump","risk":"low"}],
  "findings": [],
  "code_adaptation_required": false
}`
}

func validateAdaptAIJSON() string {
	return `{
  "summary": "Bump needs source changes.",
  "walkthrough": [{"path":"go.mod","summary":"require bump","risk":"medium"}],
  "findings": [],
  "code_adaptation_required": true
}`
}

func approvalDiffs(opts approvalOpts) []map[string]any {
	var diffs []map[string]any
	if opts.depDiff {
		diffs = append(diffs,
			map[string]any{
				"old_path": "go.mod", "new_path": "go.mod",
				"diff": "@@ -1,3 +1,4 @@\n module example\n \n+require github.com/foo v1.2.3\n",
			},
			map[string]any{
				"old_path": "go.sum", "new_path": "go.sum",
				"diff": "@@ -1,1 +1,2 @@\n github.com/foo v1.2.0 h1:abc\n+github.com/foo v1.2.3 h1:def\n",
			},
		)
	} else {
		diffs = append(diffs, map[string]any{
			"old_path": "main.go", "new_path": "main.go",
			"diff": "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n",
		})
	}
	for _, extra := range opts.extraFiles {
		switch extra {
		case "main.go":
			diffs = append(diffs, map[string]any{
				"old_path": "main.go", "new_path": "main.go",
				"diff": "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n",
			})
		case ".codereview.yml", ".codereview.yaml":
			diffs = append(diffs, map[string]any{
				"old_path": extra, "new_path": extra,
				"diff": "@@ -1,2 +1,3 @@\n review:\n   drafts: false\n+  approve_dependency_bumps: true\n",
			})
		default:
			diffs = append(diffs, map[string]any{
				"old_path": extra, "new_path": extra,
				"diff": "@@ -1,1 +1,2 @@\n keep\n+added\n",
			})
		}
	}
	return diffs
}

type countingApprovalAI struct {
	response   string
	calls      *int32
	lastSystem *string
}

func (c countingApprovalAI) Complete(_ context.Context, request ai.Request) (string, error) {
	if c.calls != nil {
		atomic.AddInt32(c.calls, 1)
	}
	if c.lastSystem != nil {
		*c.lastSystem = request.System
	}
	return c.response, nil
}
