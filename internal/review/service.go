package review

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

var issueReference = regexp.MustCompile(`(?:^|[^\w])#(\d+)\b`)

type Service struct {
	cfg          config.Config
	gitlab       *gitlab.Client
	ai           ai.Client
	instructions instructions.Loader
	state        *state.Store
	logger       *logx.Logger
}

func NewService(cfg config.Config, gitlabClient *gitlab.Client, aiClient ai.Client, loader instructions.Loader, store *state.Store, logger *logx.Logger) *Service {
	return &Service{cfg: cfg, gitlab: gitlabClient, ai: aiClient, instructions: loader, state: store, logger: logger}
}

// ErrBlockingFindings is returned when the review completed but configured
// blocking findings require the CI job to fail.
var ErrBlockingFindings = errors.New("blocking findings detected")

func (s *Service) Process(ctx context.Context, job Job) error {
	s.logger.Info("processing job", "request_id", job.RequestID, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "reason", job.Reason, "ask", job.Ask, "dry_run", s.cfg.Target.DryRun)
	origCfg := s.cfg
	origAI := s.ai
	defer func() {
		s.cfg = origCfg
		s.ai = origAI
	}()
	if job.Ask {
		return s.answerMention(ctx, job)
	}
	return s.processReview(ctx, job)
}

func (s *Service) processReview(ctx context.Context, job Job) (returnErr error) {
	started := time.Now()
	mr, err := s.gitlab.GetMergeRequest(ctx, job.ProjectID, job.MergeRequest)
	if err != nil {
		return err
	}
	if mr.State != "opened" && mr.State != "locked" {
		s.logger.Info("skipping review for non-open merge request", "request_id", job.RequestID, "state", mr.State)
		return nil
	}
	current := s.state.Get(job.ProjectID, job.MergeRequest)

	headSHA := mr.SHA
	if headSHA == "" {
		headSHA = mr.DiffRefs.HeadSHA
	}
	if headSHA == "" {
		return errors.New("merge request head SHA is not available yet")
	}
	mr.SHA = headSHA
	// Policy is reviewed-SHA content and applies before draft skip, diffs, or AI spend.
	if err := s.applyReviewedRepoPolicy(ctx, mr); err != nil {
		return err
	}
	if reason := s.skipReason(mr); reason != "" {
		if job.Force && strings.HasPrefix(reason, "disabled by") {
			s.logger.Info("title/label disable overridden by force review", "request_id", job.RequestID, "reason", reason)
		} else {
			if strings.HasPrefix(reason, "disabled by") && s.cfg.Review.SkipAck && !s.cfg.Target.DryRun {
				if err := s.ackDisabledReview(ctx, job, mr, reason); err != nil {
					s.logger.Warn("could not post disabled-review note", "request_id", job.RequestID, "error", err)
				}
			}
			s.logger.Info("skipping review", "request_id", job.RequestID, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "reason", reason)
			return nil
		}
	}
	mode := EffectiveReviewMode(s.cfg.Target.Mode, s.cfg.Review.DefaultMode)
	publishStatus := s.cfg.Review.CommitStatus && modeIncluded(mode, s.cfg.Review.StatusReviewModes)
	if current.LastReviewedSHA == headSHA && !job.Force {
		// Same-SHA skip must still honor @mention skip commands. The model is not called.
		_ = s.applyThreadCommands(ctx, job, nil)
		s.logger.Info("skipping review; head already reviewed", "request_id", job.RequestID, "sha", shortSHA(headSHA))
		return nil
	}
	progressID := int64(0)
	walkthroughFinalized := false
	if !s.cfg.Target.DryRun {
		if err := s.preflightGitLabWrites(ctx, job, mr, headSHA, publishStatus, current.WalkthroughID, &progressID); err != nil {
			return err
		}
	}
	defer func() {
		if s.cfg.Target.DryRun {
			return
		}
		if returnErr == nil || errors.Is(returnErr, ErrBlockingFindings) {
			return
		}
		if walkthroughFinalized {
			s.logger.Error("review published but a later step failed", "request_id", job.RequestID, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "error", returnErr)
			return
		}
		s.logger.Error("review failed", "request_id", job.RequestID, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "error", returnErr)
		if publishStatus && !job.SummaryOnly {
			s.setStatus(ctx, mr, job.RequestID, "failed", "CodeReview failed; see merge request")
		}
		if progressID != 0 {
			requestLabel := "this review"
			if job.RequestID != "" {
				requestLabel = fmt.Sprintf("request `%s`", job.RequestID)
			}
			_, _ = s.gitlab.UpdateNote(ctx, job.ProjectID, job.MergeRequest, progressID, appendWalkthroughMarker("## CodeReview walkthrough\n\n❌ The review failed. Check the CodeReview CI job logs for "+requestLabel+" and the provider error.\n\n<sub>No review state was committed; a retry is safe.</sub>"))
		}
	}()

	incremental := mode != ModeDeep && mode != ModeSecurity && !job.Full && current.LastReviewedSHA != "" && current.LastReviewedSHA != headSHA && s.cfg.Review.AutoIncremental
	var rawDiffs []gitlab.Diff
	var notices []string
	if incremental {
		rawDiffs, err = s.gitlab.Compare(ctx, job.ProjectID, current.LastReviewedSHA, headSHA)
		if err != nil {
			s.logger.Warn("incremental compare failed; falling back to full MR diff", "request_id", job.RequestID, "error", err)
			notices = append(notices, "Incremental comparison failed, so the complete merge request diff was reviewed.")
			incremental = false
		}
	}
	if !incremental {
		rawDiffs, err = s.gitlab.ListMergeRequestDiffs(ctx, job.ProjectID, job.MergeRequest, s.cfg.Review.MaxDiffFiles*2)
		if err != nil {
			return err
		}
	}
	prepared, limitNotices := PrepareDiffs(rawDiffs, s.cfg.Review.IgnorePaths, s.cfg.Review.MaxDiffFiles, s.cfg.Review.MaxDiffChars)
	notices = append(notices, limitNotices...)
	fullPrepared := prepared
	if incremental {
		fullRaw, fullErr := s.gitlab.ListMergeRequestDiffs(ctx, job.ProjectID, job.MergeRequest, s.cfg.Review.MaxDiffFiles*2)
		if fullErr != nil {
			s.logger.Warn("could not load full merge request diff for thread identity; using incremental hunk only", "request_id", job.RequestID, "error", fullErr)
		} else {
			fullPrepared, _ = PrepareDiffs(fullRaw, s.cfg.Review.IgnorePaths, s.cfg.Review.MaxDiffFiles, s.cfg.Review.MaxDiffChars)
		}
	}
	if len(prepared) == 0 {
		leftover := 0
		if incremental && !job.SummaryOnly && !s.cfg.Target.DryRun {
			leftover = s.leftoverFromGitLab(ctx, job, nil, fullPrepared)
		}
		if leftover > 0 {
			notices = append(notices, fmt.Sprintf("%d finding(s) from earlier reviews remain open.", leftover))
		}
		if s.cfg.Target.DryRun {
			s.logger.Info("dry-run: would publish no-reviewable-changes result",
				"request_id", job.RequestID,
				"findings", 0,
				"walkthrough_notes", boolCount(s.cfg.Review.PostSummary),
				"inline_discussions", 0,
				"description_summaries", 0,
				"commit_statuses", boolCount(publishStatus && !job.SummaryOnly),
				"would_block", false,
			)
			return nil
		}
		needsChanges := leftover > 0
		details := ReviewDetails{Mode: mode, OpenPriorFindings: leftover}
		body := FormatWalkthrough(AIReview{Summary: "No reviewable changes in this increment."}, nil, notices, incremental, time.Since(started), details, s.cfg.Review.CommentStyle, s.cfg.Review.BlockingSeverities)
		if leftover == 0 {
			body = appendWalkthroughMarker("## CodeReview walkthrough\n\n✅ No reviewable changes were found after applying the configured path and size filters.")
		}
		if s.cfg.Review.PostSummary {
			var walkthroughErr error
			progressID, walkthroughErr = s.upsertWalkthrough(ctx, mr, progressID, body, job.RequestID)
			if walkthroughErr != nil {
				return walkthroughErr
			}
			walkthroughFinalized = true
		}
		if !job.SummaryOnly {
			if publishStatus {
				status, description := "success", "No reviewable changes found"
				if needsChanges {
					status, description = "failed", "Needs changes"
				}
				if err := s.setStatus(ctx, mr, job.RequestID, status, description); err != nil {
					return err
				}
			}
			return s.completeState(job, headSHA, progressID, true)
		}
		return nil
	}

	policyChanged := PolicyFileChanged(prepared)
	if s.cfg.Review.ApproveDependencyBumps && policyChanged && !config.RepoEnvSet("GITLAB_APPROVE_DEPENDENCY_BUMPS") {
		s.cfg.Review.ApproveDependencyBumps = false
		notices = append(notices, "Auto-approve for dependency bumps was ignored because this merge request changes `.codereview.yml` or `.codereview.yaml` and `GITLAB_APPROVE_DEPENDENCY_BUMPS` is not set by the operator.")
	}
	yamlBumpBlocked := policyChanged && !config.RepoEnvSet("REVIEW_DEPENDENCY_BUMP_MODE")
	if yamlBumpBlocked && s.cfg.Review.DependencyBumpMode == config.DependencyBumpModeValidate {
		notices = append(notices, "Yaml-driven dependency bump auto-approve was ignored because this merge request changes `.codereview.yml` or `.codereview.yaml` and `REVIEW_DEPENDENCY_BUMP_MODE` is not set by the operator.")
	}
	rawDepOnly := DependencyOnlyChange(prepared)
	hasDeps := HasDependencyChange(prepared)
	pushURLSet := strings.TrimSpace(s.cfg.Review.BumpPushURL) != ""
	if s.cfg.Review.DependencyBumpMode == config.DependencyBumpModeValidate && !yamlBumpBlocked {
		s.cfg.Review.ApproveDependencyBumps = true
	}
	depFeatures := s.cfg.Review.DependencyBumps || s.cfg.Review.ApproveDependencyBumps || s.cfg.Review.DependencyBumpMode != "" || pushURLSet
	depOnly := depFeatures && rawDepOnly
	if depFeatures && depOnly {
		notices = append(notices, "This merge request only changes dependency manifests/lockfiles.")
	}
	if depFeatures && !depOnly && s.cfg.Review.ApproveDependencyBumps {
		notices = append(notices, "Auto-approve for dependency bumps does not apply because this merge request changes files other than dependency manifests/lockfiles.")
	}
	decision := DecideBumpPolicy(BumpPolicyInput{
		Mode:                 s.cfg.Review.DependencyBumpMode,
		DependencyOnly:       rawDepOnly,
		HasDependencyChange:  hasDeps,
		ManualRequest:        isManualBumpRequest(job),
		YamlPrivilegeBlocked: yamlBumpBlocked,
		PushURLSet:           pushURLSet,
		DryRun:               s.cfg.Target.DryRun,
	})
	if decision.Notice != "" {
		notices = append(notices, decision.Notice)
	}
	if decision.SkipModel {
		return s.finishBumpWithoutModel(ctx, job, mr, headSHA, &progressID, &walkthroughFinalized, started, mode, prepared, notices, decision)
	}

	loadedInstructions, err := s.instructions.Load()
	if err != nil {
		return err
	}
	repositoryContext := s.loadContext(ctx, mr, prepared, mode)
	if strings.TrimSpace(job.ExtraFocus) != "" {
		notices = append(notices, "This review included an additional untrusted @mention request as extra focus. It cannot override the review protocol.")
	}
	useDepProtocol := depOnly && (s.cfg.Review.DependencyBumps || decision.Mode == config.DependencyBumpModeValidate || decision.NeedAgentReport)
	requests := BuildReviewRequests(loadedInstructions, mr, prepared, repositoryContext, s.cfg.Review.PathInstructions, s.cfg.AI.MaxInputChars, incremental, mode, useDepProtocol, job.ExtraFocus)
	if suffix := bumpPromptSuffix(decision); suffix != "" {
		for i := range requests {
			requests[i].System += suffix
		}
	}
	if len(requests) == 0 {
		return errors.New("review prompt builder produced no requests")
	}
	parts := make([]AIReview, 0, len(requests))
	for _, request := range requests {
		raw, completionErr := s.ai.Complete(ctx, request)
		if completionErr != nil {
			return completionErr
		}
		parsed, parseErr := ParseAIReview(raw)
		if parseErr != nil && s.cfg.AI.JSONRepair {
			repaired, repairErr := s.ai.Complete(ctx, RepairRequest(loadedInstructions, raw))
			if repairErr == nil {
				parsed, parseErr = ParseAIReview(repaired)
			}
		}
		if parseErr != nil {
			return parseErr
		}
		parts = append(parts, parsed)
		if s.cfg.Review.DeepVerification && (mode == ModeDeep || mode == ModeSecurity) {
			verifiedRaw, verifyErr := s.ai.Complete(ctx, BuildVerificationRequest(request, mode))
			if verifyErr != nil {
				return fmt.Errorf("%s verification pass: %w", mode, verifyErr)
			}
			verified, verifyParseErr := ParseAIReview(verifiedRaw)
			if verifyParseErr != nil && s.cfg.AI.JSONRepair {
				repaired, repairErr := s.ai.Complete(ctx, RepairRequest(loadedInstructions, verifiedRaw))
				if repairErr == nil {
					verified, verifyParseErr = ParseAIReview(repaired)
				}
			}
			if verifyParseErr != nil {
				return fmt.Errorf("parse %s verification pass: %w", mode, verifyParseErr)
			}
			verified.Summary = ""
			verified.Walkthrough = nil
			parts = append(parts, verified)
		}
	}
	result := MergeReviews(parts)
	result.IssueAssessments = ValidateIssueAssessments(result.IssueAssessments, repositoryContext.Issues)
	if len(repositoryContext.Issues) > 0 && len(result.IssueAssessments) == 0 {
		notices = append(notices, "Linked issues were included in the prompt, but the model returned no issue assessments.")
	}
	decision = DecideBumpPolicy(BumpPolicyInput{
		Mode:                 s.cfg.Review.DependencyBumpMode,
		DependencyOnly:       rawDepOnly,
		HasDependencyChange:  hasDeps,
		ManualRequest:        isManualBumpRequest(job),
		YamlPrivilegeBlocked: yamlBumpBlocked,
		CodeMustChange:       codeMustChangeFromReview(rawDepOnly, result),
		PushURLSet:           pushURLSet,
		DryRun:               s.cfg.Target.DryRun,
	})
	if decision.NeedAgentReport {
		if strings.TrimSpace(result.AgentReport) == "" {
			return errors.New("dependency bump report was empty")
		}
		result.BranchName = resolveAdaptBranchName(result, mr.SourceBranch, mr.IID)
		notices = append(notices, "Suggested adaptation branch: `"+result.BranchName+"`.")
	}
	if decision.Notice != "" {
		notices = append(notices, decision.Notice)
	}
	if decision.Mode == config.DependencyBumpModeValidate && !decision.AllowApprove {
		s.cfg.Review.ApproveDependencyBumps = false
	}
	findings, rejected := ValidateFindings(result.Findings, prepared, s.cfg.Review.MinimumConfidence, s.cfg.Review.MaxComments)
	if len(rejected) > 0 {
		notices = append(notices, fmt.Sprintf("%d model suggestion(s) were suppressed by line, confidence, duplication, or comment-limit validation.", len(rejected)))
	}

	if s.cfg.Target.DryRun {
		blocked := s.cfg.Review.BlockOnFindings && modeIncluded(mode, s.cfg.Review.BlockingReviewModes) && HasBlockingFinding(findings, s.cfg.Review.BlockingSeverities)
		needsChanges := HasBlockingFinding(findings, s.cfg.Review.BlockingSeverities)
		s.logDryRunPublication(job, findings, result, publishStatus, blocked)
		_ = s.applyThreadCommands(ctx, job, nil)
		if notice, err := s.syncApproval(ctx, job, needsChanges, depOnly); err != nil {
			return err
		} else if notice != "" {
			s.logger.Info("dry-run approval", "request_id", job.RequestID, "notice", notice)
		}
		return nil
	}

	if s.cfg.Review.PostInline && !job.SummaryOnly {
		notices, findings = s.publishInlineFindings(ctx, job, mr, findings, prepared, fullPrepared, incremental, notices)
	}
	blocked := s.cfg.Review.BlockOnFindings && modeIncluded(mode, s.cfg.Review.BlockingReviewModes) && HasBlockingFinding(findings, s.cfg.Review.BlockingSeverities)
	needsChanges := HasBlockingFinding(findings, s.cfg.Review.BlockingSeverities)
	openPrior := 0
	if incremental && !job.SummaryOnly {
		openPrior = s.leftoverFromGitLab(ctx, job, findings, fullPrepared)
		if openPrior > 0 {
			needsChanges = true
			notices = append(notices, fmt.Sprintf("%d finding(s) from earlier reviews remain open.", openPrior))
		}
	}

	if s.cfg.Review.SummaryInDescription && strings.TrimSpace(result.Summary) != "" {
		latestMR, latestErr := s.gitlab.GetMergeRequest(ctx, job.ProjectID, job.MergeRequest)
		if latestErr != nil {
			notices = append(notices, "The merge request description summary was skipped because the latest description could not be loaded safely.")
			s.logger.Warn("failed to reload merge request before description update", "request_id", job.RequestID, "error", latestErr)
		} else if updated := UpdateDescription(latestMR.Description, result.Summary, s.cfg.Review.Mention); updated != latestMR.Description {
			if err := s.gitlab.UpdateMergeRequestDescription(ctx, job.ProjectID, job.MergeRequest, updated); err != nil {
				s.logger.Warn("failed to update merge request description", "request_id", job.RequestID, "error", err)
				notices = append(notices, "The merge request description summary could not be updated; the walkthrough still contains the review.")
			}
		}
	}

	if notice, err := s.syncApproval(ctx, job, needsChanges, depOnly); err != nil {
		return err
	} else if notice != "" {
		notices = append(notices, notice)
	}

	details := ReviewDetails{
		Mode: mode, Provider: s.cfg.AI.Provider, EURouting: s.cfg.OpenRouter.EURouting,
		Files: len(prepared), DiffChars: preparedDiffChars(prepared), ContextFiles: len(repositoryContext.Files),
		TreeEntries: len(repositoryContext.Tree), LinkedIssues: len(repositoryContext.Issues), ModelBatches: len(requests),
		Verification: s.cfg.Review.DeepVerification && (mode == ModeDeep || mode == ModeSecurity), SuppressedFindings: len(rejected),
		OpenPriorFindings: openPrior,
	}
	walkthrough := FormatWalkthrough(result, findings, notices, incremental, time.Since(started), details, s.cfg.Review.CommentStyle, s.cfg.Review.BlockingSeverities)
	if job.SummaryOnly {
		if !s.cfg.Review.SummaryInDescription || s.cfg.Review.PostSummary {
			body := "## CodeReview summary\n\n" + sanitizeModelText(result.Summary)
			if progressID != 0 {
				_, err = s.gitlab.UpdateNote(ctx, job.ProjectID, job.MergeRequest, progressID, body)
			} else {
				_, err = s.gitlab.CreateNote(ctx, job.ProjectID, job.MergeRequest, body)
			}
		}
		return err
	}
	if s.cfg.Review.PostSummary {
		var walkthroughErr error
		progressID, walkthroughErr = s.upsertWalkthrough(ctx, mr, progressID, walkthrough, job.RequestID)
		if walkthroughErr != nil {
			return walkthroughErr
		}
		walkthroughFinalized = true
	}
	if decision.AllowPush {
		if err := s.postBumpPush(ctx, mr, headSHA, prepared, result); err != nil {
			return err
		}
		s.logger.Info("sent dependency bump webhook", "request_id", job.RequestID, "repo", mergeRequestRepo(mr), "suggested_branch", result.BranchName)
	}
	if publishStatus {
		status := "success"
		description := "Ready to merge"
		if needsChanges {
			status = "failed"
			description = "Needs changes"
		}
		if err := s.setStatus(ctx, mr, job.RequestID, status, description); err != nil {
			return err
		}
	}
	// Blocking reviews must not cache the head; a STATE_PATH retry would skip and exit 0.
	if err := s.completeState(job, headSHA, progressID, !blocked); err != nil {
		return err
	}
	s.logger.Info("review published",
		"request_id", job.RequestID,
		"project_id", job.ProjectID,
		"mr_iid", job.MergeRequest,
		"sha", shortSHA(headSHA),
		"mode", mode,
		"findings", len(findings),
		"suppressed", len(rejected),
		"incremental", incremental,
		"blocked", blocked,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	if blocked {
		return ErrBlockingFindings
	}
	return nil
}

type ownedDiscussion struct {
	ID            string
	NoteID        int64
	FindingID     string
	Title         string
	Path          string
	Line          int
	LineContentID string
	HasLocation   bool
	Unresolved    bool
}

func (s *Service) publishInlineFindings(ctx context.Context, job Job, mr gitlab.MergeRequest, findings []Finding, hunk, full []PreparedDiff, incremental bool, notices []string) ([]string, []Finding) {
	owned, listed := []ownedDiscussion{}, false
	skipped := newSkippedThreads()
	discussions, listErr := s.gitlab.ListDiscussions(ctx, job.ProjectID, job.MergeRequest)
	if listErr != nil {
		s.logger.Warn("failed to list discussions; posting inlines without dedup", "request_id", job.RequestID, "error", listErr)
		notices = append(notices, "Existing inline discussions could not be listed, so new comments may duplicate earlier findings.")
	} else {
		listed = true
		_, discussions, notices = s.applyThreadCommandsTo(ctx, job, discussions, notices)
		skipped = collectSkippedThreads(discussions, s.threadCommandAliases())
		skipped.backfillFromDiffs(full)
		skipped.indexCurrentLines(full)
		owned = ownedInlineDiscussions(discussions, full)
	}

	refs, refErr := s.diffRefs(ctx, mr)
	if refErr != nil {
		return append(notices, "Inline comments could not be positioned because the current diff version was unavailable."), findings
	}

	updated := map[string]struct{}{}
	newPosted := 0
	skippedPosted := 0
	remaining := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		if skipped.hasFinding(finding, full) {
			skippedPosted++
			continue
		}
		remaining = append(remaining, finding)
		if listed {
			if match, ok := matchOwnedDiscussion(owned, finding, updated, full); ok {
				updated[match.ID] = struct{}{}
				if match.NoteID == 0 {
					s.logger.Warn("owned inline discussion is missing a finding note id", "request_id", job.RequestID, "discussion_id", match.ID, "path", finding.Path, "line", finding.Line)
					notices = append(notices, fmt.Sprintf("Could not update the inline discussion for `%s:%d`; it remains listed in the walkthrough.", finding.Path, finding.Line))
					continue
				}
				if _, err := s.gitlab.UpdateNote(ctx, job.ProjectID, job.MergeRequest, match.NoteID, FormatFinding(finding, s.cfg.Review.CommentStyle)); err != nil {
					s.logger.Warn("failed to update inline finding", "request_id", job.RequestID, "path", finding.Path, "line", finding.Line, "error", err)
					notices = append(notices, fmt.Sprintf("Could not update the inline discussion for `%s:%d`; it remains listed in the walkthrough.", finding.Path, finding.Line))
				}
				continue
			}
		}
		if s.cfg.Review.MaxComments >= 0 && newPosted >= s.cfg.Review.MaxComments {
			continue
		}
		preparedDiff, _ := findPreparedDiff(hunk, finding.Path)
		if preparedDiff.Diff.NewPath == "" {
			preparedDiff, _ = findPreparedDiff(full, finding.Path)
		}
		position := gitlab.DiffPosition{
			PositionType: "text",
			BaseSHA:      refs.BaseSHA,
			StartSHA:     refs.StartSHA,
			HeadSHA:      refs.HeadSHA,
			OldPath:      preparedDiff.Diff.OldPath,
			NewPath:      preparedDiff.Diff.NewPath,
			NewLine:      finding.Line,
		}
		if _, postErr := s.gitlab.CreateDiffDiscussion(ctx, job.ProjectID, job.MergeRequest, FormatFinding(finding, s.cfg.Review.CommentStyle), position); postErr != nil {
			s.logger.Warn("failed to post inline finding", "request_id", job.RequestID, "path", finding.Path, "line", finding.Line, "error", postErr)
			notices = append(notices, fmt.Sprintf("Could not post the inline discussion for `%s:%d`; it remains listed in the walkthrough.", finding.Path, finding.Line))
			continue
		}
		newPosted++
	}
	if listed {
		notices = s.resolveStaleInlineDiscussions(ctx, job, owned, remaining, hunk, full, incremental, updated, notices)
	}
	if skippedPosted > 0 {
		notices = append(notices, fmt.Sprintf("%d previously skipped finding(s) were left resolved.", skippedPosted))
	}
	return notices, remaining
}

func (s *Service) leftoverFromGitLab(ctx context.Context, job Job, findings []Finding, full []PreparedDiff) int {
	discussions, err := s.gitlab.ListDiscussions(ctx, job.ProjectID, job.MergeRequest)
	if err != nil {
		return 0
	}
	owned := ownedInlineDiscussions(discussions, full)
	skipped := collectSkippedThreads(discussions, s.threadCommandAliases())
	skipped.backfillFromDiffs(full)
	skipped.indexCurrentLines(full)
	return leftoverOpenCount(owned, skipped, findings, full)
}

func (s *Service) resolveStaleInlineDiscussions(ctx context.Context, job Job, owned []ownedDiscussion, findings []Finding, hunk, full []PreparedDiff, incremental bool, used map[string]struct{}, notices []string) []string {
	current := make(map[string]Finding, len(findings))
	for _, finding := range findings {
		current[FindingID(finding)] = finding
	}
	failed := 0
	staleIDs := map[string]struct{}{}
	for _, discussion := range staleOwnedDiscussions(owned, current, findings, hunk, full, incremental) {
		if _, ok := used[discussion.ID]; ok {
			continue
		}
		staleIDs[discussion.ID] = struct{}{}
	}
	for i := range owned {
		if _, stale := staleIDs[owned[i].ID]; !stale {
			continue
		}
		if err := s.gitlab.ResolveDiscussion(ctx, job.ProjectID, job.MergeRequest, owned[i].ID); err != nil {
			s.logger.Warn("failed to resolve stale inline discussion", "request_id", job.RequestID, "discussion_id", owned[i].ID, "error", err)
			failed++
			continue
		}
		owned[i].Unresolved = false
	}
	if failed > 0 {
		notices = append(notices, fmt.Sprintf("Could not resolve %d previous inline discussion(s) that no longer match the current findings; they remain open.", failed))
	}
	return notices
}

func ownedInlineDiscussions(discussions []gitlab.Discussion, diffs []PreparedDiff) []ownedDiscussion {
	var result []ownedDiscussion
	for _, discussion := range discussions {
		id, noteID, ok := discussionFindingNote(discussion)
		if !ok {
			continue
		}
		item := ownedDiscussion{
			ID:         discussion.ID,
			NoteID:     noteID,
			FindingID:  id,
			Title:      discussionFindingTitle(discussion),
			Unresolved: discussionUnresolved(discussion),
		}
		if path, line, ok := discussionLocation(discussion); ok {
			item.Path, item.Line, item.HasLocation = path, line, true
		}
		if contentID, ok := discussionLineContentID(discussion); ok {
			item.LineContentID = contentID
		} else if item.HasLocation {
			item.LineContentID = lineContentIDFromDiffs(diffs, item.Path, item.Line)
		}
		result = append(result, item)
	}
	return result
}

func discussionLocation(discussion gitlab.Discussion) (string, int, bool) {
	for _, note := range discussion.Notes {
		if note.Position == nil || note.Position.NewLine <= 0 {
			continue
		}
		path := note.Position.NewPath
		if path == "" {
			path = note.Position.OldPath
		}
		if path == "" {
			continue
		}
		return path, note.Position.NewLine, true
	}
	return "", 0, false
}

func discussionFindingTitle(discussion gitlab.Discussion) string {
	for _, note := range discussion.Notes {
		if _, ok := parseFindingID(note.Body); ok {
			if title := parseFindingTitle(note.Body); title != "" {
				return title
			}
		}
	}
	return ""
}

func discussionFindingNote(discussion gitlab.Discussion) (id string, noteID int64, ok bool) {
	for _, note := range discussion.Notes {
		if parsed, found := parseFindingID(note.Body); found {
			return parsed, note.ID, true
		}
	}
	return "", 0, false
}

func discussionFindingID(discussion gitlab.Discussion) (string, bool) {
	id, _, ok := discussionFindingNote(discussion)
	return id, ok
}

func discussionUnresolved(discussion gitlab.Discussion) bool {
	for _, note := range discussion.Notes {
		if note.Resolvable && !note.Resolved {
			return true
		}
	}
	return false
}

func unresolvedFindingIDs(owned []ownedDiscussion) map[string]struct{} {
	ids, _ := unresolvedInlineKeys(owned)
	return ids
}

func unresolvedInlineKeys(owned []ownedDiscussion) (ids, lines map[string]struct{}) {
	ids = make(map[string]struct{}, len(owned))
	lines = make(map[string]struct{}, len(owned))
	for _, discussion := range owned {
		if !discussion.Unresolved {
			continue
		}
		ids[discussion.FindingID] = struct{}{}
		if discussion.HasLocation {
			lines[findingLineKey(discussion.Path, discussion.Line)] = struct{}{}
		}
	}
	return ids, lines
}

func findingLineKey(path string, line int) string {
	return path + "\x00" + strconv.Itoa(line)
}

func matchOwnedDiscussion(owned []ownedDiscussion, finding Finding, used map[string]struct{}, diffs []PreparedDiff) (ownedDiscussion, bool) {
	id := FindingID(finding)
	lineKey := findingLineKey(finding.Path, finding.Line)
	contentKeys := contentKeysAroundFinding(finding, diffs)
	var idMatch, lineMatch, contentMatch ownedDiscussion
	hasID, hasLine, hasContent := false, false, false
	for _, discussion := range owned {
		if !discussion.Unresolved {
			continue
		}
		if _, seen := used[discussion.ID]; seen {
			continue
		}
		if discussion.FindingID == id && !hasID {
			idMatch = discussion
			hasID = true
		}
		if discussion.HasLocation && findingLineKey(discussion.Path, discussion.Line) == lineKey && !hasLine {
			lineMatch = discussion
			hasLine = true
		}
		if !hasContent && discussion.LineContentID != "" {
			want := contentKey(discussion.Path, discussion.LineContentID)
			for _, key := range contentKeys {
				if key == want {
					contentMatch = discussion
					hasContent = true
					break
				}
			}
		}
	}
	if hasContent {
		return contentMatch, true
	}
	if hasID {
		return idMatch, true
	}
	if hasLine {
		return lineMatch, true
	}
	return uniqueNearOwnedDiscussion(owned, finding, used)
}

func staleOwnedDiscussions(owned []ownedDiscussion, current map[string]Finding, findings []Finding, hunk, full []PreparedDiff, incremental bool) []ownedDiscussion {
	var stale []ownedDiscussion
	for _, discussion := range owned {
		if !discussion.Unresolved {
			continue
		}
		matched := false
		if _, exists := current[discussion.FindingID]; exists {
			matched = true
		}
		if !matched {
			for _, finding := range findings {
				if discussionMatchesFinding(discussion, finding, full) {
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}
		if incremental && !hunkContainsDiscussion(hunk, discussion) && fullDiffStillHasDiscussion(full, discussion) {
			continue
		}
		stale = append(stale, discussion)
	}
	return stale
}

func (s *Service) preflightGitLabWrites(ctx context.Context, job Job, mr gitlab.MergeRequest, headSHA string, publishStatus bool, walkthroughID int64, progressID *int64) error {
	if !gitlabWritesEnabled(s.cfg.Review, job, publishStatus) {
		return nil
	}
	probed := false
	if s.identityWritesEnabled() {
		if _, err := s.requireHumanGitLabUser(ctx); err != nil {
			return gitlabWritePreflightError(err)
		}
		if err := s.ensureReviewer(ctx, job, mr); err != nil {
			return gitlabWritePreflightError(err)
		}
		probed = true
	}
	if publishStatus && !job.SummaryOnly {
		if err := s.setStatus(ctx, mr, job.RequestID, "pending", "AI review in progress"); err != nil {
			return gitlabWritePreflightError(err)
		}
		probed = true
	}
	if s.cfg.Review.PostSummary {
		body := fmt.Sprintf("## CodeReview walkthrough\n\nReviewing `%s`.\n\n<sub>Triggered by %s.</sub>", shortSHA(headSHA), job.Reason)
		if s.cfg.Review.PostProgress {
			body = fmt.Sprintf("## CodeReview walkthrough\n\n⏳ Reviewing `%s`…\n\n<sub>Triggered by %s.</sub>", shortSHA(headSHA), job.Reason)
		}
		id, err := s.upsertWalkthrough(ctx, mr, walkthroughID, appendWalkthroughMarker(body), job.RequestID)
		if err != nil {
			return gitlabWritePreflightError(err)
		}
		*progressID = id
		probed = true
	}
	if probed {
		return nil
	}
	return errors.New("GitLab writes are enabled but no pre-AI write probe is available; enable POST_WALKTHROUGH or GITLAB_COMMIT_STATUS_ENABLED to verify write access before the model call. CI_JOB_TOKEN is often insufficient for notes/discussions; set GITLAB_TOKEN to a project or group access token with api scope")
}

func gitlabWritesEnabled(cfg config.ReviewConfig, job Job, publishStatus bool) bool {
	if cfg.ApproveOnClean || cfg.RequestReview || cfg.ApproveDependencyBumps || cfg.DependencyBumpMode == config.DependencyBumpModeValidate {
		return true
	}
	if cfg.PostSummary || cfg.SummaryInDescription {
		return true
	}
	if !job.SummaryOnly && (cfg.PostInline || publishStatus) {
		return true
	}
	return false
}

func gitlabWritePreflightError(err error) error {
	return fmt.Errorf("GitLab write preflight failed before the model call: %w; CI_JOB_TOKEN is often insufficient for notes/discussions — set GITLAB_TOKEN to a project or group access token with api scope", err)
}

func (s *Service) logDryRunPublication(job Job, findings []Finding, result AIReview, publishStatus, blocked bool) {
	inline := 0
	if s.cfg.Review.PostInline && !job.SummaryOnly {
		inline = len(findings)
	}
	s.logger.Info("dry-run: skipping GitLab publication",
		"request_id", job.RequestID,
		"findings", len(findings),
		"walkthrough_notes", boolCount(s.cfg.Review.PostSummary),
		"inline_discussions", inline,
		"description_summaries", boolCount(s.cfg.Review.SummaryInDescription && strings.TrimSpace(result.Summary) != ""),
		"commit_statuses", boolCount(publishStatus && !job.SummaryOnly),
		"would_block", blocked,
	)
	if blocked {
		s.logger.Info("dry-run: blocking findings would have failed CI", "request_id", job.RequestID)
	}
}

func boolCount(enabled bool) int {
	if enabled {
		return 1
	}
	return 0
}

const (
	repoPolicyFileYML  = ".codereview.yml"
	repoPolicyFileYAML = ".codereview.yaml"
)

func (s *Service) applyReviewedRepoPolicy(ctx context.Context, mr gitlab.MergeRequest) error {
	raw, path, err := s.readRepoPolicyFile(ctx, mr)
	if err != nil {
		return err
	}
	if raw != nil {
		parsed, err := config.ParseRepoFile(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		prevAI := s.cfg.AI
		if err := s.cfg.ApplyRepoFile(parsed); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if aiClientConfigChanged(prevAI, s.cfg.AI) {
			s.ai = ai.New(s.cfg, s.logger)
		}
		s.logger.Info("loaded repository review policy", "path", path, "sha", shortSHA(mr.SHA))
	}
	return s.applyIgnoreFile(ctx, mr)
}

func aiClientConfigChanged(before, after config.AIConfig) bool {
	if before.Provider != after.Provider || before.APIKey != after.APIKey || before.BaseURL != after.BaseURL || before.AuthMode != after.AuthMode {
		return true
	}
	if len(before.Models) != len(after.Models) {
		return true
	}
	for index := range before.Models {
		if before.Models[index] != after.Models[index] {
			return true
		}
	}
	return false
}

func (s *Service) applyIgnoreFile(ctx context.Context, mr gitlab.MergeRequest) error {
	for _, path := range []string{ignoreFileName, ignoreFileAltName} {
		content, err := s.gitlab.GetFile(ctx, mr.ProjectID, path, mr.SHA)
		if gitlab.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("fetch %s at %s: %w", path, shortSHA(mr.SHA), err)
		}
		patterns := ParseIgnoreFile(content)
		if len(patterns) == 0 {
			s.logger.Info("loaded empty ignore file", "path", path)
			return nil
		}
		s.cfg.Review.IgnorePaths = append(patterns, s.cfg.Review.IgnorePaths...)
		s.logger.Info("loaded review ignore file", "path", path, "patterns", len(patterns), "sha", shortSHA(mr.SHA))
		return nil
	}
	return nil
}

func (s *Service) readRepoPolicyFile(ctx context.Context, mr gitlab.MergeRequest) ([]byte, string, error) {
	for _, path := range []string{repoPolicyFileYML, repoPolicyFileYAML} {
		content, err := s.gitlab.GetFile(ctx, mr.ProjectID, path, mr.SHA)
		if gitlab.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("fetch %s at %s: %w", path, shortSHA(mr.SHA), err)
		}
		return []byte(content), path, nil
	}
	return nil, "", nil
}

func (s *Service) skipReason(mr gitlab.MergeRequest) string {
	if mr.Draft && !s.cfg.Review.ReviewDrafts {
		return "draft"
	}
	if token, ok := mrDisableToken(mr, s.cfg.Review.SkipTokens); ok {
		return "disabled by title or label (" + token + ")"
	}
	for _, alias := range s.cfg.Review.MentionAliases {
		ignoreCommand := "@" + strings.ToLower(alias) + " ignore"
		if strings.Contains(strings.ToLower(mr.Description), ignoreCommand) {
			return "ignored by merge request description"
		}
	}
	for _, author := range s.cfg.Review.IgnoreAuthors {
		if strings.EqualFold(author, mr.Author.Username) {
			return "ignored author"
		}
	}
	return ""
}

func mrDisableToken(mr gitlab.MergeRequest, tokens []string) (string, bool) {
	title := strings.ToLower(mr.Title)
	for _, token := range tokens {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		if strings.Contains(title, token) {
			return token, true
		}
		for _, label := range mr.Labels {
			if strings.EqualFold(strings.TrimSpace(label), token) || strings.Contains(strings.ToLower(label), token) {
				return token, true
			}
		}
	}
	return "", false
}

func (s *Service) ackDisabledReview(ctx context.Context, job Job, mr gitlab.MergeRequest, reason string) error {
	notes, err := s.gitlab.ListMergeRequestNotes(ctx, job.ProjectID, job.MergeRequest)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if strings.Contains(note.Body, disabledMarker) {
			return nil
		}
	}
	body := "## CodeReview\n\nReview is disabled for this merge request (" + reason + "). Remove the title/label token to review on the next pipeline, or comment `@codereview review`.\n\n<sub>Generated by CodeReview.</sub>\n" + disabledMarker
	_, err = s.gitlab.CreateNote(ctx, job.ProjectID, job.MergeRequest, body)
	return err
}

func (s *Service) loadContext(ctx context.Context, mr gitlab.MergeRequest, diffs []PreparedDiff, mode ReviewMode) RepositoryContext {
	result := RepositoryContext{Files: map[string]string{}}
	var repositoryTree []gitlab.TreeEntry
	needTree := s.cfg.Review.IncludeRepositoryTree || s.cfg.Review.IncludeRepositoryGuides || (s.cfg.Review.IncludeRelatedFiles && mode != ModeQuick)
	if needTree {
		tree, err := s.gitlab.ListTree(ctx, mr.ProjectID, mr.SHA, s.cfg.Review.MaxTreeEntries)
		if err != nil {
			s.logger.Warn("failed to load repository tree", "error", err)
		} else {
			repositoryTree = tree
			if s.cfg.Review.IncludeRepositoryTree && mode != ModeQuick {
				result.Tree = tree
			}
		}
	}
	paths := make([]string, 0, len(s.cfg.Review.ContextFiles)+len(diffs))
	paths = append(paths, s.cfg.Review.ContextFiles...)
	if s.cfg.Review.IncludeRepositoryGuides {
		paths = append(paths, repositoryGuidelinePaths(repositoryTree, s.cfg.Review.RepositoryGuidePatterns)...)
	}
	if s.cfg.Review.IncludeChangedFileContent && mode != ModeQuick {
		for _, diff := range diffs {
			if !diff.Diff.DeletedFile {
				paths = append(paths, diff.Diff.NewPath)
			}
		}
	}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if len(result.Files) >= s.cfg.Review.MaxContextFiles {
			break
		}
		if _, exists := seen[path]; exists || matchesAny(path, s.cfg.Review.IgnorePaths) {
			continue
		}
		seen[path] = struct{}{}
		content, err := s.gitlab.GetFile(ctx, mr.ProjectID, path, mr.SHA)
		if err != nil {
			if !gitlab.IsNotFound(err) {
				s.logger.Debug("failed to fetch context file", "path", path, "error", err)
			}
			continue
		}
		if strings.ContainsRune(content, '\x00') {
			continue
		}
		result.Files[path] = truncate(content, s.cfg.Review.MaxFileContentChars)
	}
	if s.cfg.Review.IncludeRelatedFiles && mode != ModeQuick && len(result.Files) < s.cfg.Review.MaxContextFiles {
		for _, related := range relatedContextPaths(repositoryTree, diffs, result.Files, s.cfg.Review.MaxRelatedContextFiles) {
			if len(result.Files) >= s.cfg.Review.MaxContextFiles {
				break
			}
			if _, exists := seen[related]; exists || matchesAny(related, s.cfg.Review.IgnorePaths) {
				continue
			}
			seen[related] = struct{}{}
			content, err := s.gitlab.GetFile(ctx, mr.ProjectID, related, mr.SHA)
			if err != nil || strings.ContainsRune(content, '\x00') {
				continue
			}
			result.Files[related] = truncate(content, s.cfg.Review.MaxFileContentChars)
		}
	}
	if s.cfg.Review.IncludeLinkedIssues && mode != ModeQuick {
		seenIssues := map[int64]struct{}{}
		for _, match := range issueReference.FindAllStringSubmatch(mr.Title+"\n"+mr.Description, 10) {
			id, _ := strconv.ParseInt(match[1], 10, 64)
			if id == 0 {
				continue
			}
			if _, exists := seenIssues[id]; exists {
				continue
			}
			seenIssues[id] = struct{}{}
			issue, err := s.gitlab.GetIssue(ctx, mr.ProjectID, id)
			if err == nil {
				result.Issues = append(result.Issues, issue)
			}
			if len(result.Issues) >= 5 {
				break
			}
		}
	}
	return result
}

func repositoryGuidelinePaths(tree []gitlab.TreeEntry, patterns []string) []string {
	var result []string
	for _, entry := range tree {
		if entry.Type == "blob" && matchesAny(entry.Path, patterns) {
			result = append(result, entry.Path)
		}
	}
	sort.Strings(result)
	return result
}

var relativeImport = regexp.MustCompile(`(?m)(?:from\s+["']|import\s*(?:\(|)["']|require\s*\(\s*["'])(\.{1,2}/[^"']+)`)

func relatedContextPaths(tree []gitlab.TreeEntry, diffs []PreparedDiff, files map[string]string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	available := map[string]struct{}{}
	for _, entry := range tree {
		if entry.Type == "blob" {
			available[entry.Path] = struct{}{}
		}
	}
	changed := map[string]struct{}{}
	for _, diff := range diffs {
		changed[diff.Diff.NewPath] = struct{}{}
	}
	changedPaths := make([]string, 0, len(changed))
	for changedPath := range changed {
		changedPaths = append(changedPaths, changedPath)
	}
	sort.Strings(changedPaths)
	seen := map[string]struct{}{}
	var result []string
	add := func(candidate string) {
		candidate = path.Clean(strings.TrimPrefix(candidate, "./"))
		if len(result) >= limit {
			return
		}
		if _, exists := available[candidate]; !exists {
			return
		}
		if _, isChanged := changed[candidate]; isChanged {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	for _, changedPath := range changedPaths {
		ext := path.Ext(changedPath)
		stem := strings.TrimSuffix(changedPath, ext)
		if strings.HasSuffix(stem, "_test") {
			add(strings.TrimSuffix(stem, "_test") + ext)
		} else {
			add(stem + "_test" + ext)
		}
		for _, suffix := range []string{".test", ".spec"} {
			if strings.HasSuffix(stem, suffix) {
				add(strings.TrimSuffix(stem, suffix) + ext)
			} else {
				add(stem + suffix + ext)
			}
		}
		content := files[changedPath]
		for _, match := range relativeImport.FindAllStringSubmatch(content, -1) {
			base := path.Clean(path.Join(path.Dir(changedPath), match[1]))
			add(base)
			for _, candidateExt := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs"} {
				add(base + candidateExt)
				add(path.Join(base, "index"+candidateExt))
			}
		}
		if len(result) >= limit {
			break
		}
	}
	return result
}

func preparedDiffChars(diffs []PreparedDiff) int {
	total := 0
	for _, diff := range diffs {
		total += len(diff.Diff.Diff)
	}
	return total
}

func modeIncluded(mode ReviewMode, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(candidate, string(mode)) {
			return true
		}
	}
	return false
}

func (s *Service) diffRefs(ctx context.Context, mr gitlab.MergeRequest) (gitlab.DiffRefs, error) {
	if mr.DiffRefs.BaseSHA != "" && mr.DiffRefs.StartSHA != "" && mr.DiffRefs.HeadSHA != "" {
		return mr.DiffRefs, nil
	}
	version, err := s.gitlab.LatestDiffVersion(ctx, mr.ProjectID, mr.IID)
	if err != nil {
		return gitlab.DiffRefs{}, err
	}
	return gitlab.DiffRefs{BaseSHA: version.BaseCommitSHA, StartSHA: version.StartCommitSHA, HeadSHA: version.HeadCommitSHA}, nil
}

func (s *Service) upsertWalkthrough(ctx context.Context, mr gitlab.MergeRequest, noteID int64, body, requestID string) (int64, error) {
	body = appendWalkthroughMarker(body)
	if noteID != 0 {
		if note, err := s.gitlab.UpdateNote(ctx, mr.ProjectID, mr.IID, noteID, body); err == nil {
			return note.ID, nil
		} else {
			s.logger.Warn("failed to update walkthrough note; creating a new one", "request_id", requestID, "note_id", noteID, "error", err)
		}
	} else if recoveredID := s.recoverWalkthroughID(ctx, mr, requestID); recoveredID != 0 {
		if note, err := s.gitlab.UpdateNote(ctx, mr.ProjectID, mr.IID, recoveredID, body); err == nil {
			s.persistWalkthroughID(mr.ProjectID, mr.IID, note.ID, requestID)
			return note.ID, nil
		} else {
			s.logger.Warn("failed to update recovered walkthrough note; creating a new one", "request_id", requestID, "note_id", recoveredID, "error", err)
		}
	}
	note, err := s.gitlab.CreateNote(ctx, mr.ProjectID, mr.IID, body)
	if err != nil {
		s.logger.Warn("failed to create walkthrough note", "request_id", requestID, "error", err)
		return 0, err
	}
	s.persistWalkthroughID(mr.ProjectID, mr.IID, note.ID, requestID)
	return note.ID, nil
}

func (s *Service) recoverWalkthroughID(ctx context.Context, mr gitlab.MergeRequest, requestID string) int64 {
	notes, err := s.gitlab.ListMergeRequestNotes(ctx, mr.ProjectID, mr.IID)
	if err != nil {
		s.logger.Warn("failed to list merge request notes; creating a new walkthrough", "request_id", requestID, "error", err)
		return 0
	}
	return latestOwnedWalkthroughID(notes)
}

func (s *Service) persistWalkthroughID(projectID, iid, noteID int64, requestID string) {
	if err := s.state.Update(projectID, iid, func(value *state.MRState) { value.WalkthroughID = noteID }); err != nil {
		s.logger.Warn("failed to persist walkthrough note id", "request_id", requestID, "note_id", noteID, "error", err)
	}
}

func (s *Service) completeState(job Job, headSHA string, walkthroughID int64, advanceSHA bool) error {
	return s.state.Update(job.ProjectID, job.MergeRequest, func(value *state.MRState) {
		if advanceSHA {
			value.LastReviewedSHA = headSHA
		}
		if walkthroughID != 0 {
			value.WalkthroughID = walkthroughID
		}
	})
}

func (s *Service) setStatus(ctx context.Context, mr gitlab.MergeRequest, requestID, status, description string) error {
	if err := s.gitlab.SetCommitStatus(ctx, mr.ProjectID, mr.SHA, status, s.cfg.Review.ReviewStatusName, truncate(description, 255), mr.WebURL); err != nil {
		s.logger.Warn("failed to set GitLab commit status", "request_id", requestID, "status", status, "error", err)
		return err
	}
	return nil
}

func shortSHA(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}
