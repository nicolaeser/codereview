package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/review"
	"github.com/nicolaeser/codereview/internal/state"
	"github.com/nicolaeser/codereview/internal/webhook"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(runHealth())
	}
	os.Exit(run(context.Background()))
}

func runHealth() int {
	listen := strings.TrimSpace(os.Getenv("WEBHOOK_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:8090"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/health", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(parent context.Context) int {
	cfg, err := config.Load()
	if err != nil {
		logx.New(os.Stderr, "error").Error("invalid configuration", "error", err)
		return 1
	}
	settings, err := webhook.LoadSettings()
	if err != nil {
		logx.New(os.Stderr, "error").Error("invalid webhook configuration", "error", err)
		return 1
	}

	logger := logx.New(os.Stdout, cfg.LogLevel)
	loader := instructions.Loader{DefaultPath: cfg.Instructions.DefaultPath, AdditionalPath: cfg.Instructions.AdditionalPath}
	if _, err := loader.Load(); err != nil {
		logger.Error("instructions are not readable", "error", err)
		return 1
	}
	store, err := state.Open(cfg.State.Path)
	if err != nil {
		logger.Error("state store could not be opened", "error", err)
		return 1
	}

	gitlabClient := gitlab.NewWithOptions(cfg.GitLab.APIURL, cfg.GitLab.Token, cfg.GitLab.Timeout, cfg.GitLab.UseJobToken, cfg.GitLab.MaxRetries)
	service := review.NewService(cfg, gitlabClient, ai.New(cfg, logger), loader, store, logger)
	srv := webhook.New(cfg, settings, serviceRunner{service: service, gitlab: gitlabClient}, gitlabClient, logger)

	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if host, _, err := net.SplitHostPort(settings.Listen); err == nil && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		logger.Warn("webhook listen address is not loopback; keep it on an internal network")
	}
	logger.Info("webhook listening",
		"addr", settings.Listen,
		"path", settings.Path,
		"handle_mr", settings.HandleMR,
		"handle_notes", settings.HandleNotes,
		"dry_run", cfg.Target.DryRun,
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("webhook server failed", "error", err)
			return 1
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.JobTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("webhook shutdown failed", "error", err)
			return 1
		}
		logger.Info("webhook stopped")
	}
	return 0
}

type serviceRunner struct {
	service *review.Service
	gitlab  *gitlab.Client
}

func (r serviceRunner) Process(ctx context.Context, job review.Job) error {
	return r.service.Process(ctx, job)
}

func (r serviceRunner) SkipDiscussion(ctx context.Context, projectID, iid int64, discussionID, noteBody, author string) error {
	if discussionID == "" {
		return errors.New("discussion id is required to skip")
	}
	if _, err := r.gitlab.ReplyToDiscussion(ctx, projectID, iid, discussionID, review.SkipReplyBody()); err != nil {
		return err
	}
	return r.gitlab.ResolveDiscussion(ctx, projectID, iid, discussionID)
}

func (r serviceRunner) ReplyHelp(ctx context.Context, projectID, iid int64, discussionID, mention string) error {
	body := review.HelpMarkdown(mention)
	if discussionID != "" {
		_, err := r.gitlab.ReplyToDiscussion(ctx, projectID, iid, discussionID, body)
		return err
	}
	_, err := r.gitlab.CreateNote(ctx, projectID, iid, body)
	return err
}

func (r serviceRunner) ResolveOwned(ctx context.Context, projectID, iid int64) error {
	_, err := r.service.ResolveOwnedFindings(ctx, projectID, iid)
	return err
}
