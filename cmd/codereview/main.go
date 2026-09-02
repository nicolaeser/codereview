package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/review"
	"github.com/nicolaeser/codereview/internal/state"
)

const (
	exitOK      = 0
	exitFailure = 1
	exitBlocked = 2
	exitUsage   = 2
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(parent context.Context, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "todos":
			return runTodos(parent, args[1:])
		case "login":
			return runLogin(parent, args[1:])
		case "logout":
			return runLogout(parent, args[1:])
		}
	}
	fs := flag.NewFlagSet("codereview", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `CodeReview — one-shot debug CLI for GitLab merge requests

Usage:
  codereview [flags]
  codereview todos [flags]
  codereview login grok|xai|codex
  codereview logout [grok|codex]

The product process is codereview-webhook. This CLI reviews one MR and exits.
Override project and MR with flags, or PROJECT_ID / MERGE_REQUEST_IID.

codereview todos drains pending @mention todos for the token user.
Requires a personal access token.
codereview login stores a Grok (xAI device code) or Codex credential locally.
The webhook process uses AI_KEYS_FILE / AI_KEYS_JSON, not home login files.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Environment (common):
  CI_PROJECT_ID / PROJECT_ID / --project
  CI_MERGE_REQUEST_IID / MERGE_REQUEST_IID / --mr
  GITLAB_API_URL or CI_API_V4_URL
  GITLAB_TOKEN (preferred) or CI_JOB_TOKEN
  AI_PROVIDER, AI_API_KEY, AI_MODEL
  JOB_TIMEOUT (default 20m)
  REVIEW_DRY_RUN / --dry-run

Exit codes:
  0  review completed, intentionally skipped, or successful dry-run
  1  configuration or runtime failure
  2  blocking findings (BLOCK_ON_FINDINGS=true; not used for --dry-run)

See docs/CONFIGURATION.md.
`)
	}
	projectID := fs.Int64("project", 0, "GitLab project ID (defaults to PROJECT_ID / CI_PROJECT_ID)")
	mergeRequest := fs.Int64("mr", 0, "Merge request IID (defaults to MERGE_REQUEST_IID / CI_MERGE_REQUEST_IID)")
	mode := fs.String("mode", "", "Review mode: quick, standard, deep, or security")
	full := fs.Bool("full", false, "Review the complete merge request diff instead of incremental")
	force := fs.Bool("force", false, "Re-review even when the head SHA was already reviewed")
	summaryOnly := fs.Bool("summary-only", false, "Publish only the high-level summary")
	dryRun := fs.Bool("dry-run", false, "Fetch, score, and validate without publishing GitLab writes or advancing review state")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		logx.New(os.Stderr, "error").Error("invalid configuration", "error", err)
		return exitFailure
	}
	cfg.ApplyTargetOverrides(*projectID, *mergeRequest, *mode, *full, *force, *summaryOnly, *dryRun, flagWasSet(fs, "full"), flagWasSet(fs, "force"), flagWasSet(fs, "summary-only"), flagWasSet(fs, "dry-run"))
	if err := cfg.ValidateTarget(); err != nil {
		logx.New(os.Stderr, "error").Error("missing merge request target", "error", err)
		return exitFailure
	}

	logger := logx.New(os.Stdout, cfg.LogLevel)
	loader := instructions.Loader{DefaultPath: cfg.Instructions.DefaultPath, AdditionalPath: cfg.Instructions.AdditionalPath}
	if _, err := loader.Load(); err != nil {
		logger.Error("instructions are not readable", "error", err)
		return exitFailure
	}
	store, err := state.Open(cfg.State.Path)
	if err != nil {
		logger.Error("state store could not be opened", "error", err)
		return exitFailure
	}

	gitlabClient := gitlab.NewWithOptions(cfg.GitLab.APIURL, cfg.GitLab.Token, cfg.GitLab.Timeout, cfg.GitLab.UseJobToken, cfg.GitLab.MaxRetries)
	aiClient := ai.New(cfg, logger)
	service := review.NewService(cfg, gitlabClient, aiClient, loader, store, logger)

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.JobTimeout)
	defer cancel()

	reviewMode := firstNonEmpty(cfg.Target.Mode, cfg.Review.DefaultMode)
	effective := review.EffectiveReviewMode(reviewMode, cfg.Review.DefaultMode)
	job := review.Job{
		ProjectID:    cfg.Target.ProjectID,
		MergeRequest: cfg.Target.MergeRequest,
		RequestID:    requestID(cfg),
		Full:         cfg.Target.Full || effective == review.ModeDeep || effective == review.ModeSecurity,
		SummaryOnly:  cfg.Target.SummaryOnly,
		Force:        cfg.Target.Force,
		Reason:       "one-shot CLI",
		Mode:         reviewMode,
	}

	authMode := "private-token"
	if cfg.GitLab.UseJobToken {
		authMode = "job-token"
	}
	logger.Info("starting review",
		"project_id", job.ProjectID,
		"mr_iid", job.MergeRequest,
		"mode", job.Mode,
		"full", job.Full,
		"force", job.Force,
		"dry_run", cfg.Target.DryRun,
		"job_timeout", cfg.JobTimeout,
		"gitlab_auth", authMode,
		"provider", cfg.AI.Provider,
		"request_id", job.RequestID,
	)
	if err := service.Process(ctx, job); err != nil {
		if errors.Is(err, review.ErrBlockingFindings) {
			if cfg.Target.DryRun {
				logger.Info("dry-run completed; blocking findings would have failed CI", "exit_code", exitOK)
				return exitOK
			}
			logger.Error("review completed with blocking findings", "error", err, "exit_code", exitBlocked)
			return exitBlocked
		}
		logger.Error("review failed", "error", err, "exit_code", exitFailure)
		return exitFailure
	}
	logger.Info("review completed successfully",
		"project_id", job.ProjectID,
		"mr_iid", job.MergeRequest,
		"request_id", job.RequestID,
		"exit_code", exitOK,
	)
	return exitOK
}

func runTodos(parent context.Context, args []string) int {
	fs := flag.NewFlagSet("codereview todos", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "List matching mention todos without reviewing or marking them done")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		logx.New(os.Stderr, "error").Error("invalid configuration", "error", err)
		return exitFailure
	}
	if flagWasSet(fs, "dry-run") {
		cfg.Target.DryRun = *dryRun
	}
	logger := logx.New(os.Stdout, cfg.LogLevel)
	loader := instructions.Loader{DefaultPath: cfg.Instructions.DefaultPath, AdditionalPath: cfg.Instructions.AdditionalPath}
	if _, err := loader.Load(); err != nil {
		logger.Error("instructions are not readable", "error", err)
		return exitFailure
	}
	store, err := state.Open(cfg.State.Path)
	if err != nil {
		logger.Error("state store could not be opened", "error", err)
		return exitFailure
	}
	gitlabClient := gitlab.NewWithOptions(cfg.GitLab.APIURL, cfg.GitLab.Token, cfg.GitLab.Timeout, cfg.GitLab.UseJobToken, cfg.GitLab.MaxRetries)
	service := review.NewService(cfg, gitlabClient, ai.New(cfg, logger), loader, store, logger)
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.JobTimeout)
	defer cancel()
	logger.Info("draining mention todos", "todo_max", cfg.Review.TodoMax, "dry_run", cfg.Target.DryRun)
	result, err := service.DrainMentionTodos(ctx)
	if err != nil {
		logger.Error("mention todo drain failed", "error", err, "reviewed", result.Reviewed, "marked", result.Marked, "skipped", result.Skipped)
		return exitFailure
	}
	logger.Info("mention todo drain completed", "reviewed", result.Reviewed, "marked", result.Marked, "skipped", result.Skipped, "exit_code", exitOK)
	return exitOK
}

func requestID(cfg config.Config) string {
	if pipeline := os.Getenv("CI_PIPELINE_ID"); pipeline != "" {
		return fmt.Sprintf("ci-%s-%s", pipeline, os.Getenv("CI_JOB_ID"))
	}
	return fmt.Sprintf("ci-%d-%d-%d", cfg.Target.ProjectID, cfg.Target.MergeRequest, time.Now().UTC().Unix())
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
