package review

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

var mergeRequestURL = regexp.MustCompile(`/merge_requests/(\d+)\b`)

type TodoDrainResult struct {
	Reviewed int
	Marked   int
	Skipped  int
}

func (s *Service) DrainMentionTodos(ctx context.Context) (TodoDrainResult, error) {
	var result TodoDrainResult
	todos, err := s.gitlab.ListTodos(ctx, s.cfg.Review.TodoMax*4)
	if err != nil {
		return result, fmt.Errorf("list GitLab todos: %w", err)
	}
	aliases := s.cfg.Review.MentionAliases
	if len(aliases) == 0 && s.cfg.Review.Mention != "" {
		aliases = []string{s.cfg.Review.Mention}
	}
	for _, todo := range todos {
		if result.Reviewed >= s.cfg.Review.TodoMax {
			break
		}
		if !todoIsMention(todo) || !todoMentionsBot(todo, aliases) {
			result.Skipped++
			continue
		}
		projectID, iid, ok := todoMergeRequest(todo)
		if !ok {
			result.Skipped++
			continue
		}
		req := ParseMentionRequest(todo.Body, aliases)
		if s.cfg.Target.DryRun {
			switch req.Command {
			case CommandHelp:
				s.logger.Info("dry-run: would post command list from mention todo", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid)
			case CommandAsk:
				s.logger.Info("dry-run: would answer mention todo question", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid)
			case CommandResolve:
				s.logger.Info("dry-run: would resolve owned findings from mention todo", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid)
			default:
				s.logger.Info("dry-run: would review merge request from mention todo", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid)
			}
			result.Reviewed++
			continue
		}
		if req.Command == CommandHelp {
			if _, err := s.gitlab.CreateNote(ctx, projectID, iid, HelpMarkdown(s.cfg.Review.Mention)); err != nil {
				s.logger.Error("mention todo command list failed; todo left pending", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid, "error", err)
				return result, err
			}
			result.Reviewed++
			if err := s.gitlab.MarkTodoDone(ctx, todo.ID); err != nil {
				return result, fmt.Errorf("mark todo %d done: %w", todo.ID, err)
			}
			result.Marked++
			continue
		}
		if req.Command == CommandResolve {
			if _, err := s.ResolveOwnedFindings(ctx, projectID, iid); err != nil {
				s.logger.Error("mention todo resolve failed; todo left pending", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid, "error", err)
				return result, err
			}
			result.Reviewed++
			if err := s.gitlab.MarkTodoDone(ctx, todo.ID); err != nil {
				return result, fmt.Errorf("mark todo %d done: %w", todo.ID, err)
			}
			result.Marked++
			continue
		}
		job := Job{
			ProjectID:    projectID,
			MergeRequest: iid,
			RequestID:    fmt.Sprintf("todo-%d", todo.ID),
			Force:        req.Command != CommandSkip,
			Full:         req.Command == CommandFullReview || req.Command == CommandNone,
			Ask:          req.Command == CommandAsk,
			ExtraFocus:   req.Extra,
			Reason:       "GitLab mention todo",
			Mode:         firstNonEmptyMode(s.cfg.Target.Mode, s.cfg.Review.DefaultMode),
		}
		if err := s.Process(ctx, job); err != nil {
			s.logger.Error("mention todo review failed; todo left pending", "todo_id", todo.ID, "project_id", projectID, "mr_iid", iid, "error", err)
			return result, err
		}
		result.Reviewed++
		if err := s.gitlab.MarkTodoDone(ctx, todo.ID); err != nil {
			return result, fmt.Errorf("mark todo %d done: %w", todo.ID, err)
		}
		result.Marked++
	}
	return result, nil
}

func todoIsMention(todo gitlab.Todo) bool {
	action := strings.ToLower(strings.TrimSpace(todo.ActionName))
	return action == "mentioned" || action == "directly_addressed"
}

func todoMentionsBot(todo gitlab.Todo, aliases []string) bool {
	haystack := strings.ToLower(todo.Body + "\n" + todo.TargetURL)
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(alias, "@")))
		if alias == "" {
			continue
		}
		if strings.Contains(haystack, "@"+alias) {
			return true
		}
	}
	return false
}

func todoMergeRequest(todo gitlab.Todo) (projectID, iid int64, ok bool) {
	projectID = todo.Project.ID
	if projectID == 0 {
		projectID = todo.Target.ProjectID
	}
	if strings.EqualFold(todo.TargetType, "MergeRequest") && todo.Target.IID > 0 && projectID > 0 {
		return projectID, todo.Target.IID, true
	}
	if match := mergeRequestURL.FindStringSubmatch(todo.TargetURL); len(match) == 2 {
		parsed, err := strconv.ParseInt(match[1], 10, 64)
		if err == nil && parsed > 0 && projectID > 0 {
			return projectID, parsed, true
		}
	}
	return 0, 0, false
}

func firstNonEmptyMode(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return string(ModeStandard)
}
