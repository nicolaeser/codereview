package review

import (
	"strings"
	"testing"

	"github.com/nicolaeser/codereview/internal/config"
)

func TestValidateLockfileOnlyAllowsApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode: config.DependencyBumpModeValidate,
		depDiff:  true,
		finding:  lowFinding(),
		user:     "coder",
		aiJSON:   validateCleanAIJSON(),
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 1 {
		t.Fatalf("approves = %d, want 1", stats.approves)
	}
	if stats.pushPosts != 0 {
		t.Fatalf("validate must not PUSH, posts=%d", stats.pushPosts)
	}
}

func TestValidateSourceChangeBlocksApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode:   config.DependencyBumpModeValidate,
		depDiff:    true,
		extraFiles: []string{"main.go"},
		finding:    lowFinding(),
		user:       "coder",
		aiJSON:     validateCleanAIJSON(),
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 0 || stats.unapproves != 0 {
		t.Fatalf("approves=%d unapproves=%d", stats.approves, stats.unapproves)
	}
	if stats.pushPosts != 0 {
		t.Fatalf("no PUSH URL must not POST, posts=%d", stats.pushPosts)
	}
}

func TestValidateOmittedClassificationDoesNotApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode: config.DependencyBumpModeValidate,
		depDiff:  true,
		finding:  lowFinding(),
		user:     "coder",
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 0 {
		t.Fatalf("omitted code_adaptation_required must not auto-approve, approves=%d", stats.approves)
	}
}

func TestValidateDoesNotLeaveApproveFlagOnService(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode: config.DependencyBumpModeValidate,
		depDiff:  true,
		finding:  lowFinding(),
		user:     "coder",
		aiJSON:   validateCleanAIJSON(),
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 1 {
		t.Fatalf("approves = %d, want 1", stats.approves)
	}
	if stats.approveFlagAfter {
		t.Fatal("validate must not leave ApproveDependencyBumps enabled on the service")
	}
}

func TestValidateAnalysisCodeMustChangeBlocksApprove(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode: config.DependencyBumpModeValidate,
		depDiff:  true,
		finding:  lowFinding(),
		user:     "coder",
		aiJSON:   validateAdaptAIJSON(),
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 0 {
		t.Fatalf("approves = %d, want 0", stats.approves)
	}
	if stats.pushPosts != 0 {
		t.Fatalf("PUSH posts = %d, want 0", stats.pushPosts)
	}
	if !strings.Contains(strings.ToLower(stats.noteBody), "source adaptation is required") {
		t.Fatalf("expected adaptation notice, body=%s", stats.noteBody)
	}
}

func TestNotifySkipsModel(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		bumpMode:  config.DependencyBumpModeNotify,
		depDiff:   true,
		finding:   lowFinding(),
		user:      "coder",
		automatic: true,
		reason:    "GitLab webhook",
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
	if stats.approves != 0 || stats.pushPosts != 0 {
		t.Fatalf("notify must not approve or PUSH: approves=%d posts=%d", stats.approves, stats.pushPosts)
	}
	if !strings.Contains(stats.noteBody, "New versions were found") {
		t.Fatalf("expected notify note, body=%s", stats.noteBody)
	}
	if !strings.Contains(stats.noteBody, "`go.mod`") {
		t.Fatalf("expected changed dependency files, body=%s", stats.noteBody)
	}
}

func TestWaitSkipsApproveAndPushUntilMention(t *testing.T) {
	waiting := runApprovalReview(t, approvalOpts{
		bumpMode:  config.DependencyBumpModeWait,
		depDiff:   true,
		finding:   lowFinding(),
		user:      "coder",
		automatic: true,
		reason:    "GitLab webhook",
		pushURL:   "httptest",
	})
	if waiting.err != nil {
		t.Fatalf("automatic wait Process() error = %v", waiting.err)
	}
	if waiting.aiCalls != 0 {
		t.Fatalf("wait must not call the model before mention, AI=%d", waiting.aiCalls)
	}
	if waiting.approves != 0 || waiting.pushPosts != 0 {
		t.Fatalf("wait must not approve or PUSH: approves=%d posts=%d", waiting.approves, waiting.pushPosts)
	}
	if !strings.Contains(waiting.noteBody, "waiting for an explicit") {
		t.Fatalf("expected wait note, body=%s", waiting.noteBody)
	}

	mentioned := runApprovalReview(t, approvalOpts{
		bumpMode:   config.DependencyBumpModeWait,
		depDiff:    true,
		finding:    lowFinding(),
		user:       "coder",
		reason:     "GitLab mention webhook",
		pushURL:    "httptest",
		pushSecret: "push-secret",
		aiJSON:     agentReportAIJSON(),
	})
	if mentioned.err != nil {
		t.Fatalf("mention wait Process() error = %v", mentioned.err)
	}
	if mentioned.aiCalls == 0 {
		t.Fatal("mention must run the review")
	}
	if mentioned.approves != 0 {
		t.Fatalf("wait must still not auto-approve after mention, approves=%d", mentioned.approves)
	}
	if mentioned.pushPosts != 1 {
		t.Fatalf("mention + operator PUSH URL must POST, posts=%d", mentioned.pushPosts)
	}
}

func TestOperatorPushURLSendsRepoMRBranchAndReport(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		depDiff:    true,
		finding:    lowFinding(),
		user:       "coder",
		aiJSON:     agentReportAIJSON(),
		pushURL:    "httptest",
		pushSecret: "push-secret",
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.pushPosts != 1 {
		t.Fatalf("PUSH posts = %d, want 1", stats.pushPosts)
	}
	if stats.pushToken != "push-secret" {
		t.Fatalf("PUSH token = %q", stats.pushToken)
	}
	for _, want := range []string{
		`"repo":"group/proj"`,
		`"merge_request_iid":34`,
		`"source_branch":"dependabot/go_modules/foo-1.2.3"`,
		`"target_branch":"main"`,
		`"suggested_branch":"codereview/adapt-foo-v1.2.3"`,
		`Prepare the codebase for foo v1.2.3`,
		`"go.mod"`,
	} {
		if !strings.Contains(stats.pushBody, want) {
			t.Fatalf("PUSH body missing %s: %s", want, stats.pushBody)
		}
	}
	if stats.approves != 0 {
		t.Fatalf("PUSH URL must not GitLab-approve by itself, approves=%d", stats.approves)
	}
	if !strings.Contains(stats.noteBody, "Prepare the codebase for foo v1.2.3") {
		t.Fatalf("walkthrough should include the report, body=%s", stats.noteBody)
	}
}

func TestPushAndApproveDryRunDoNotWrite(t *testing.T) {
	stats := runApprovalReview(t, approvalOpts{
		depDiff: true,
		finding: lowFinding(),
		user:    "coder",
		aiJSON:  agentReportAIJSON(),
		pushURL: "httptest",
		dryRun:  true,
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 0 || stats.pushPosts != 0 {
		t.Fatalf("dry-run wrote: approves=%d posts=%d", stats.approves, stats.pushPosts)
	}
	if stats.aiCalls == 0 {
		t.Fatal("dry-run PUSH still calls the model")
	}

	validateDry := runApprovalReview(t, approvalOpts{
		bumpMode: config.DependencyBumpModeValidate,
		depDiff:  true,
		finding:  lowFinding(),
		user:     "coder",
		aiJSON:   validateCleanAIJSON(),
		dryRun:   true,
	})
	if validateDry.err != nil {
		t.Fatalf("validate dry-run error = %v", validateDry.err)
	}
	if validateDry.approves != 0 {
		t.Fatalf("dry-run must not Approve, approves=%d", validateDry.approves)
	}
}

func TestOperatorPushURLStillSendsWhenPolicyFileChanges(t *testing.T) {
	t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "")
	stats := runApprovalReview(t, approvalOpts{
		depDiff:    true,
		extraFiles: []string{".codereview.yml"},
		finding:    lowFinding(),
		user:       "coder",
		aiJSON:     agentReportAIJSON(),
		pushURL:    "httptest",
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.pushPosts != 1 {
		t.Fatalf("operator PUSH URL must still POST when the policy file changes, posts=%d", stats.pushPosts)
	}
}

func TestPolicyFileChangeIgnoresYamlValidateApprove(t *testing.T) {
	t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "")
	t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "")
	stats := runApprovalReview(t, approvalOpts{
		bumpMode:   config.DependencyBumpModeValidate,
		depDiff:    true,
		extraFiles: []string{".codereview.yml"},
		finding:    lowFinding(),
		user:       "coder",
		aiJSON:     validateCleanAIJSON(),
	})
	if stats.err != nil {
		t.Fatalf("Process() error = %v", stats.err)
	}
	if stats.approves != 0 {
		t.Fatalf("yaml auto-approve must be ignored: approves=%d", stats.approves)
	}
	if !strings.Contains(stats.noteBody, "REVIEW_DEPENDENCY_BUMP_MODE") {
		t.Fatalf("expected yaml privilege ignore notice, body=%s", stats.noteBody)
	}
}

func TestDecideBumpPolicyModes(t *testing.T) {
	notify := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeNotify, DependencyOnly: true, HasDependencyChange: true})
	if !notify.SkipModel || notify.AllowApprove || notify.AllowPush {
		t.Fatalf("notify = %#v", notify)
	}
	wait := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeWait, DependencyOnly: true, HasDependencyChange: true})
	if !wait.SkipModel || !wait.Waiting || wait.AllowApprove || wait.AllowPush {
		t.Fatalf("wait = %#v", wait)
	}
	mentioned := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeWait, DependencyOnly: true, HasDependencyChange: true, ManualRequest: true})
	if mentioned.SkipModel || mentioned.AllowApprove || mentioned.AllowPush {
		t.Fatalf("wait mention = %#v", mentioned)
	}
	ok := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeValidate, DependencyOnly: true, HasDependencyChange: true})
	if !ok.AllowApprove || ok.AllowPush {
		t.Fatalf("validate clean = %#v", ok)
	}
	blocked := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeValidate, DependencyOnly: true, HasDependencyChange: true, CodeMustChange: true})
	if blocked.AllowApprove || blocked.AllowPush {
		t.Fatalf("validate code-must-change = %#v", blocked)
	}
	push := DecideBumpPolicy(BumpPolicyInput{HasDependencyChange: true, CodeMustChange: true, PushURLSet: true})
	if !push.AllowPush || !push.NeedAgentReport || push.AllowApprove {
		t.Fatalf("operator PUSH URL = %#v", push)
	}
	dry := DecideBumpPolicy(BumpPolicyInput{HasDependencyChange: true, PushURLSet: true, DryRun: true})
	if dry.AllowPush {
		t.Fatalf("dry-run PUSH = %#v", dry)
	}
	notifyURL := DecideBumpPolicy(BumpPolicyInput{Mode: config.DependencyBumpModeNotify, DependencyOnly: true, HasDependencyChange: true, PushURLSet: true})
	if !notifyURL.SkipModel || notifyURL.AllowPush {
		t.Fatalf("notify must not POST without a report: %#v", notifyURL)
	}
}
