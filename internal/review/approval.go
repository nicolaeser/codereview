package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
)

func (s *Service) identityWritesEnabled() bool {
	return s.cfg.Review.ApproveOnClean || s.cfg.Review.RequestReview || s.cfg.Review.ApproveDependencyBumps || s.cfg.Review.DependencyBumpMode == config.DependencyBumpModeValidate
}

func (s *Service) requireHumanGitLabUser(ctx context.Context) (gitlab.User, error) {
	user, err := s.gitlab.GetCurrentUser(ctx)
	if err != nil {
		return gitlab.User{}, fmt.Errorf("load GitLab user for approve/reviewer actions: %w", err)
	}
	if user.ID == 0 {
		return gitlab.User{}, fmt.Errorf("GitLab user id is missing; approve and reviewer actions need a personal access token")
	}
	if gitlab.IsAutomationUser(user) {
		return gitlab.User{}, fmt.Errorf("GitLab token user %q is a project/group bot and cannot approve or receive @mention todos; set GITLAB_TOKEN to a personal access token for a real user", user.Username)
	}
	return user, nil
}

func (s *Service) ensureReviewer(ctx context.Context, job Job, mr gitlab.MergeRequest) error {
	if !s.cfg.Review.RequestReview || s.cfg.Target.DryRun {
		return nil
	}
	user, err := s.requireHumanGitLabUser(ctx)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(mr.Reviewers)+1)
	seen := map[int64]struct{}{}
	for _, reviewer := range mr.Reviewers {
		if reviewer.ID == 0 {
			continue
		}
		if _, exists := seen[reviewer.ID]; exists {
			continue
		}
		seen[reviewer.ID] = struct{}{}
		ids = append(ids, reviewer.ID)
	}
	if _, exists := seen[user.ID]; exists {
		return nil
	}
	ids = append(ids, user.ID)
	if err := s.gitlab.SetMergeRequestReviewers(ctx, job.ProjectID, job.MergeRequest, ids); err != nil {
		return fmt.Errorf("add reviewer %q: %w", user.Username, err)
	}
	s.logger.Info("requested review from GitLab user", "request_id", job.RequestID, "username", user.Username)
	return nil
}

func (s *Service) syncApproval(ctx context.Context, job Job, needsChanges, dependencyOnly bool) (string, error) {
	enabled := s.cfg.Review.ApproveOnClean || s.cfg.Review.ApproveDependencyBumps
	if !enabled {
		return "", nil
	}
	wantApprove := (!needsChanges && s.cfg.Review.ApproveOnClean) || (!needsChanges && s.cfg.Review.ApproveDependencyBumps && dependencyOnly)
	user, err := s.requireHumanGitLabUser(ctx)
	if err != nil {
		return "", err
	}
	if !wantApprove && !needsChanges && s.cfg.Review.ApproveDependencyBumps && !dependencyOnly && !s.cfg.Review.ApproveOnClean {
		return "GitLab approval was not added; auto-approve for dependency bumps applies only to dependency-only merge requests.", nil
	}
	depApprove := !needsChanges && s.cfg.Review.ApproveDependencyBumps && dependencyOnly
	if s.cfg.Target.DryRun {
		if needsChanges {
			return "Dry-run: would remove this user's GitLab approval if present.", nil
		}
		if depApprove {
			return fmt.Sprintf("Dry-run: would approve this dependency-only merge request as `%s`. Merge still follows the project's approval rules.", user.Username), nil
		}
		return fmt.Sprintf("Dry-run: would approve this merge request as `%s`. Merge still follows the project's approval rules.", user.Username), nil
	}
	approvals, err := s.gitlab.GetMergeRequestApprovals(ctx, job.ProjectID, job.MergeRequest)
	if err != nil {
		return "", fmt.Errorf("load merge request approvals: %w", err)
	}
	approvedByUs := false
	for _, entry := range approvals.ApprovedBy {
		if entry.User.ID == user.ID || strings.EqualFold(entry.User.Username, user.Username) {
			approvedByUs = true
			break
		}
	}
	if needsChanges {
		if !approvedByUs {
			return fmt.Sprintf("GitLab approval left unchanged; `%s` had not approved.", user.Username), nil
		}
		if err := s.gitlab.UnapproveMergeRequest(ctx, job.ProjectID, job.MergeRequest); err != nil {
			return "", fmt.Errorf("unapprove merge request: %w", err)
		}
		return fmt.Sprintf("Removed GitLab approval from `%s` because the verdict needs changes.", user.Username), nil
	}
	if approvedByUs {
		if depApprove {
			return fmt.Sprintf("GitLab approval from `%s` was already present for this dependency-only merge request.", user.Username), nil
		}
		return fmt.Sprintf("GitLab approval from `%s` was already present.", user.Username), nil
	}
	if err := s.gitlab.ApproveMergeRequest(ctx, job.ProjectID, job.MergeRequest); err != nil {
		return "", fmt.Errorf("approve merge request: %w", err)
	}
	if depApprove {
		return fmt.Sprintf("Approved this dependency-only merge request as `%s`. Whether that is enough to merge is the project's GitLab approval rules, not CodeReview.", user.Username), nil
	}
	return fmt.Sprintf("Approved this merge request as `%s`. Whether that is enough to merge is the project's GitLab approval rules, not CodeReview.", user.Username), nil
}
