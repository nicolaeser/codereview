package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
)

// BumpPolicyInput is the classification state used to decide bump-mode side effects.
type BumpPolicyInput struct {
	Mode                 string
	DependencyOnly       bool
	HasDependencyChange  bool
	ManualRequest        bool
	YamlPrivilegeBlocked bool
	CodeMustChange       bool
	PushURLSet           bool
	DryRun               bool
}

// BumpDecision is the pure outcome of a bump mode for one merge request.
type BumpDecision struct {
	Mode            string
	Applies         bool
	SkipModel       bool
	AllowApprove    bool
	AllowPush       bool
	NeedAgentReport bool
	Waiting         bool
	Notice          string
}

func isManualBumpRequest(job Job) bool {
	if job.Force {
		return true
	}
	reason := strings.ToLower(job.Reason)
	return strings.Contains(reason, "mention")
}

// DecideBumpPolicy maps a classified dependency bump onto notify/wait/validate
// and an optional operator PUSH webhook (DEPENDENCY_BUMP_PUSH_URL).
func DecideBumpPolicy(in BumpPolicyInput) BumpDecision {
	mode := in.Mode
	if !in.HasDependencyChange && !in.DependencyOnly {
		return BumpDecision{Mode: mode}
	}
	if mode == config.DependencyBumpModeOff && !in.PushURLSet {
		return BumpDecision{}
	}
	out := BumpDecision{Mode: mode, Applies: true}
	codeMustChange := in.CodeMustChange || !in.DependencyOnly
	switch mode {
	case config.DependencyBumpModeNotify:
		if in.DependencyOnly {
			out.SkipModel = true
			out.Notice = "New versions were found in this merge request. CodeReview did not call a model (`notify` mode)."
		}
	case config.DependencyBumpModeWait:
		if in.DependencyOnly && !in.ManualRequest {
			out.SkipModel = true
			out.Waiting = true
			out.Notice = "This dependency bump is waiting for an explicit `@mention` review. Auto-approve is disabled until then."
			if in.PushURLSet {
				out.Notice = "This dependency bump is waiting for an explicit `@mention` review. Auto-approve and the outbound bump webhook are disabled until then."
			}
		}
	case config.DependencyBumpModeValidate:
		out.AllowApprove = in.DependencyOnly && !codeMustChange && !in.YamlPrivilegeBlocked
		if !out.AllowApprove && (codeMustChange || !in.DependencyOnly) {
			out.Notice = "Dependency bump `validate` mode did not auto-approve because source adaptation is required."
		}
	}
	if in.YamlPrivilegeBlocked && mode == config.DependencyBumpModeValidate {
		out.AllowApprove = false
	}
	if in.PushURLSet && !out.SkipModel {
		out.NeedAgentReport = true
		out.AllowPush = !in.DryRun
	}
	if in.DryRun {
		out.AllowPush = false
	}
	return out
}

func defaultAdaptBranchName(mrIID int64) string {
	if mrIID <= 0 {
		return "codereview/adapt-deps"
	}
	return fmt.Sprintf("codereview/adapt-deps-mr-%d", mrIID)
}

func bumpPromptSuffix(decision BumpDecision) string {
	if !decision.Applies {
		return ""
	}
	if decision.NeedAgentReport {
		return agentReportProtocol
	}
	if decision.Mode == config.DependencyBumpModeValidate {
		return bumpClassifyProtocol
	}
	return ""
}

const bumpClassifyProtocol = `

This merge request is a dependency version bump. Also include this JSON field:
"code_adaptation_required": true if application source or tests must change to adopt the new versions, otherwise false.
Do not approve in the JSON; findings only.
`

const agentReportProtocol = `

This merge request is a dependency version bump. Also include these JSON fields:
"agent_report": "a large Markdown report of every code, config, and test change an AI agent must make so the codebase can use the new versions; include file paths and concrete steps",
"branch_name": "a new git branch name for that adaptation work, not the merge request source branch",
"code_adaptation_required": true if application source or tests must change to adopt the new versions, otherwise false
The agent_report must be a concrete checklist. branch_name must be a new branch, separated from the merge request source branch. Do not approve in the JSON; findings only.
`

func dependencyPaths(diffs []PreparedDiff) []string {
	var paths []string
	seen := map[string]struct{}{}
	for _, diff := range diffs {
		path := preparedDiffPath(diff)
		if !dependencyFile(path) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func formatBumpSkipWalkthrough(decision BumpDecision, diffs []PreparedDiff, mention string, notices []string) string {
	var builder strings.Builder
	builder.WriteString("## CodeReview walkthrough\n\n")
	if decision.Waiting {
		builder.WriteString("### Waiting\n\n")
	} else {
		builder.WriteString("### New versions\n\n")
	}
	if strings.TrimSpace(decision.Notice) != "" {
		builder.WriteString(decision.Notice)
		builder.WriteString("\n\n")
	}
	paths := dependencyPaths(diffs)
	if len(paths) > 0 {
		builder.WriteString("Changed dependency files:\n\n")
		for _, path := range paths {
			fmt.Fprintf(&builder, "- `%s`\n", path)
		}
		builder.WriteByte('\n')
	}
	mention = strings.TrimPrefix(strings.TrimSpace(mention), "@")
	if mention == "" {
		mention = "codereview"
	}
	if decision.Waiting {
		fmt.Fprintf(&builder, "Comment `@%s review` on this merge request to run the review.\n\n", mention)
	}
	if len(notices) > 0 {
		builder.WriteString("### Notices\n\n")
		for _, notice := range notices {
			if strings.TrimSpace(notice) == "" || notice == decision.Notice {
				continue
			}
			builder.WriteString("- ")
			builder.WriteString(notice)
			builder.WriteByte('\n')
		}
		builder.WriteByte('\n')
	}
	builder.WriteString("CodeReview does not open version-bump merge requests and does not merge.\n")
	return appendWalkthroughMarker(builder.String())
}

func resolveAdaptBranchName(result AIReview, sourceBranch string, mrIID int64) string {
	name := strings.TrimSpace(result.BranchName)
	if name == "" || name == strings.TrimSpace(sourceBranch) {
		return defaultAdaptBranchName(mrIID)
	}
	return name
}

func codeMustChangeFromReview(depOnly bool, result AIReview) bool {
	if !depOnly {
		return true
	}
	if result.CodeAdaptationRequired == nil || *result.CodeAdaptationRequired {
		return true
	}
	return false
}

func (s *Service) finishBumpWithoutModel(ctx context.Context, job Job, mr gitlab.MergeRequest, headSHA string, progressID *int64, walkthroughFinalized *bool, started time.Time, mode ReviewMode, prepared []PreparedDiff, notices []string, decision BumpDecision) error {
	_ = started
	_ = mode
	if s.cfg.Target.DryRun {
		s.logger.Info("dry-run: dependency bump mode skipped the model",
			"request_id", job.RequestID,
			"mode", decision.Mode,
			"waiting", decision.Waiting,
		)
		return nil
	}
	body := formatBumpSkipWalkthrough(decision, prepared, s.cfg.Review.Mention, notices)
	var err error
	if s.cfg.Review.PostSummary {
		*progressID, err = s.upsertWalkthrough(ctx, mr, *progressID, body, job.RequestID)
		if err != nil {
			return err
		}
		*walkthroughFinalized = true
	} else if _, err = s.gitlab.CreateNote(ctx, job.ProjectID, job.MergeRequest, body); err != nil {
		return err
	}
	return s.completeState(job, headSHA, *progressID, true)
}
