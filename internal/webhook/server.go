// Package webhook is authenticated GitLab webhook intake; the product HTTP surface.
package webhook

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/review"
)

const (
	defaultListen          = "127.0.0.1:8090"
	defaultMaxBody         = int64(1 << 20)
	defaultPath            = "/gitlab-webhook"
	maxMemberProjects      = 500
	defaultMembershipFetch = 30 * time.Second
)

// Runner is the review side-effect surface used by the webhook handler.
type Runner interface {
	Process(ctx context.Context, job review.Job) error
	SkipDiscussion(ctx context.Context, projectID, iid int64, discussionID, noteBody, author string) error
	ReplyHelp(ctx context.Context, projectID, iid int64, discussionID, mention string) error
	ResolveOwned(ctx context.Context, projectID, iid int64) error
}

// ProjectAccess lists projects the GitLab token user is a member of.
type ProjectAccess interface {
	ListMemberProjectIDs(ctx context.Context, maxProjects int) (map[int64]struct{}, error)
}

// Settings are webhook-specific process options loaded from the environment.
type Settings struct {
	Listen       string
	Secret       string
	SigningToken string
	MaxBody      int64
	Path         string
	HandleMR     bool
	HandleNotes  bool
}

func LoadSettings() (Settings, error) {
	settings := Settings{
		Listen:       envOr("WEBHOOK_LISTEN", defaultListen),
		Secret:       strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")),
		SigningToken: strings.TrimSpace(os.Getenv("WEBHOOK_SIGNING_TOKEN")),
		MaxBody:      defaultMaxBody,
		Path:         envOr("WEBHOOK_PATH", defaultPath),
		HandleMR:     true,
		HandleNotes:  true,
	}
	if raw, ok := os.LookupEnv("WEBHOOK_MAX_BODY"); ok && strings.TrimSpace(raw) != "" {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return Settings{}, fmt.Errorf("WEBHOOK_MAX_BODY: %w", err)
		}
		settings.MaxBody = value
	}
	var err error
	if settings.HandleMR, err = envBool("WEBHOOK_HANDLE_MR", true); err != nil {
		return Settings{}, err
	}
	if settings.HandleNotes, err = envBool("WEBHOOK_HANDLE_NOTES", true); err != nil {
		return Settings{}, err
	}
	settings.Path = normalizePath(settings.Path)
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func (s Settings) Validate() error {
	var errs []error
	if strings.TrimSpace(s.Secret) == "" && strings.TrimSpace(s.SigningToken) == "" {
		errs = append(errs, errors.New("WEBHOOK_SECRET or WEBHOOK_SIGNING_TOKEN is required"))
	}
	if strings.TrimSpace(s.Listen) == "" {
		errs = append(errs, errors.New("WEBHOOK_LISTEN must not be empty"))
	}
	if s.MaxBody <= 0 {
		errs = append(errs, errors.New("WEBHOOK_MAX_BODY must be positive"))
	}
	cleaned := normalizePath(s.Path)
	if cleaned == "" || cleaned == "/" {
		errs = append(errs, errors.New("WEBHOOK_PATH must be a non-root URL path"))
	}
	if cleaned == "/health" || cleaned == "/healthz" {
		errs = append(errs, errors.New("WEBHOOK_PATH must not be /health"))
	}
	return errors.Join(errs...)
}

type memberRefresh struct {
	done chan struct{}
}

// Server is an optional GitLab webhook listener. The CI binary does not embed it.
type Server struct {
	reviewCfg     config.Config
	settings      Settings
	runner        Runner
	access        ProjectAccess
	logger        *logx.Logger
	mu            sync.Mutex
	wg            sync.WaitGroup
	httpServer    *http.Server
	memberMu      sync.Mutex
	memberIDs     map[int64]struct{}
	memberLoaded  bool
	memberRefresh *memberRefresh
}

func New(reviewCfg config.Config, settings Settings, runner Runner, access ProjectAccess, logger *logx.Logger) *Server {
	if logger == nil {
		logger = logx.New(io.Discard, "error")
	}
	if strings.TrimSpace(settings.Listen) == "" {
		settings.Listen = defaultListen
	}
	if settings.MaxBody <= 0 {
		settings.MaxBody = defaultMaxBody
	}
	settings.Path = normalizePath(settings.Path)
	if settings.Path == "" || settings.Path == "/" || settings.Path == "/health" || settings.Path == "/healthz" {
		settings.Path = defaultPath
	}
	server := &Server{reviewCfg: reviewCfg, settings: settings, runner: runner, access: access, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", server.handleHealth)
	mux.HandleFunc(settings.Path, server.handleWebhook)
	server.httpServer = &http.Server{
		Addr:              settings.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	return server
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return err
	case <-ctx.Done():
		if err != nil {
			return err
		}
		return ctx.Err()
	}
}

// waitIdle waits for in-flight reviews started by this server. Tests use it
// because merge-request reviews are acknowledged before Process returns.
func (s *Server) waitIdle() {
	s.wg.Wait()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength > s.settings.MaxBody {
		s.rejectTooLarge(w)
		return
	}
	r.Body = http.MaxBytesReader(nil, r.Body, s.settings.MaxBody)
	defer r.Body.Close()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.rejectTooLarge(w)
			return
		}
		s.skip(w, "", 0, 0, "", "body unread")
		return
	}
	if !requestAuthorized(r, raw, s.settings) {
		s.logger.Info("webhook unauthorized")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"status": "unauthorized"})
		return
	}
	parsed, err := parsePayload(raw)
	if err != nil {
		s.skip(w, "", 0, 0, "", "invalid json")
		return
	}

	kind := parsed.kind()
	projectID := parsed.projectID()
	iid := parsed.mergeRequestIID()
	action := parsed.action()
	if s.isSelfUser(parsed.User) {
		s.skip(w, kind, projectID, iid, action, "automation user")
		return
	}
	switch kind {
	case "merge_request":
		s.handleMergeRequest(w, r, parsed)
	case "note":
		s.handleNote(w, r, parsed)
	default:
		s.skip(w, kind, projectID, iid, action, "unknown object_kind")
	}
}

func (s *Server) handleMergeRequest(w http.ResponseWriter, r *http.Request, parsed payload) {
	kind := parsed.kind()
	projectID := parsed.projectID()
	iid := parsed.mergeRequestIID()
	action := parsed.action()
	if !s.settings.HandleMR {
		s.skip(w, kind, projectID, iid, action, "merge request events disabled")
		return
	}
	if run, reason := mergeRequestAction(action); !run {
		s.skip(w, kind, projectID, iid, action, reason)
		return
	}
	if projectID <= 0 || iid <= 0 {
		s.skip(w, kind, projectID, iid, action, "missing project or merge request id")
		return
	}
	if !s.projectAllowed(r.Context(), projectID) {
		s.skip(w, kind, projectID, iid, action, "not a project member")
		return
	}
	job := review.Job{
		ProjectID:    projectID,
		MergeRequest: iid,
		RequestID:    newRequestID(r, projectID, iid),
		Reason:       "GitLab webhook",
		Mode:         s.reviewCfg.Target.Mode,
	}
	s.processJob(w, kind, action, job, "would-process")
}

func (s *Server) handleNote(w http.ResponseWriter, r *http.Request, parsed payload) {
	kind := parsed.kind()
	projectID := parsed.projectID()
	iid := parsed.mergeRequestIID()
	action := parsed.action()
	if !s.settings.HandleNotes {
		s.skip(w, kind, projectID, iid, action, "note events disabled")
		return
	}
	if parsed.ObjectAttributes.System {
		s.skip(w, kind, projectID, iid, action, "system note")
		return
	}
	if parsed.noteableType() != "mergerequest" {
		s.skip(w, kind, projectID, iid, action, "noteable_type is not MergeRequest")
		return
	}
	if projectID <= 0 || iid <= 0 {
		s.skip(w, kind, projectID, iid, action, "missing project or merge request id")
		return
	}
	body := parsed.noteBody()
	aliases := s.mentionAliases()
	if !review.NoteMentionsBot(body, aliases) {
		s.skip(w, kind, projectID, iid, action, "no bot mention")
		return
	}
	if !s.projectAllowed(r.Context(), projectID) {
		s.skip(w, kind, projectID, iid, action, "not a project member")
		return
	}
	req := review.ParseMentionRequest(body, aliases)
	switch req.Command {
	case review.CommandSkip:
		s.skipDiscussion(w, kind, action, projectID, iid, parsed.discussionID(), body, parsed.User.Username)
		return
	case review.CommandResolve:
		s.resolveOwned(w, kind, action, projectID, iid)
		return
	case review.CommandFullReview, review.CommandReview:
		job := review.Job{
			ProjectID:    projectID,
			MergeRequest: iid,
			RequestID:    newRequestID(r, projectID, iid),
			Full:         req.Command == review.CommandFullReview,
			Force:        true,
			Reason:       "GitLab mention webhook",
			Mode:         s.reviewCfg.Target.Mode,
			ExtraFocus:   req.Extra,
		}
		s.processJob(w, kind, action, job, "would-process")
		return
	case review.CommandAsk:
		job := review.Job{
			ProjectID:    projectID,
			MergeRequest: iid,
			RequestID:    newRequestID(r, projectID, iid),
			Ask:          true,
			ExtraFocus:   req.Extra,
			DiscussionID: parsed.discussionID(),
			Reason:       "GitLab mention webhook",
			Mode:         s.reviewCfg.Target.Mode,
		}
		s.processJob(w, kind, action, job, "would-answer")
		return
	case review.CommandHelp:
		s.replyHelp(w, kind, action, projectID, iid, parsed.discussionID())
		return
	default:
		s.skip(w, kind, projectID, iid, action, "mention without a command")
		return
	}
}

func (s *Server) resolveOwned(w http.ResponseWriter, kind, action string, projectID, iid int64) {
	if s.reviewCfg.Target.DryRun {
		s.logger.Info("webhook skipped", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32), "reason", "would-resolve-owned")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "dry-run"})
		return
	}
	if !s.mu.TryLock() {
		s.skip(w, kind, projectID, iid, action, "busy")
		return
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout())
	defer cancel()
	if err := s.runner.ResolveOwned(ctx, projectID, iid); err != nil {
		s.logger.Error("webhook resolve owned failed", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "error", err)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "resolve-failed"})
		return
	}
	s.logger.Info("webhook resolved owned findings", "object_kind", kind, "project_id", projectID, "mr_iid", iid)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) replyHelp(w http.ResponseWriter, kind, action string, projectID, iid int64, discussionID string) {
	if s.reviewCfg.Target.DryRun {
		s.logger.Info("webhook skipped", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32), "reason", "would-post-help")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "dry-run"})
		return
	}
	if !s.mu.TryLock() {
		s.skip(w, kind, projectID, iid, action, "busy")
		return
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout())
	defer cancel()
	if err := s.runner.ReplyHelp(ctx, projectID, iid, discussionID, s.reviewCfg.Review.Mention); err != nil {
		s.logger.Error("webhook command list failed", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "error", err)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "help-failed"})
		return
	}
	s.logger.Info("webhook posted command list", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) skipDiscussion(w http.ResponseWriter, kind, action string, projectID, iid int64, discussionID, noteBody, author string) {
	if s.reviewCfg.Target.DryRun {
		s.logger.Info("webhook skipped", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32), "reason", "would-skip-discussion")
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "dry-run"})
		return
	}
	if !s.mu.TryLock() {
		s.skip(w, kind, projectID, iid, action, "busy")
		return
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout())
	defer cancel()
	if err := s.runner.SkipDiscussion(ctx, projectID, iid, discussionID, noteBody, author); err != nil {
		s.logger.Error("webhook skip discussion failed", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "error", err)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "skip-failed"})
		return
	}
	s.logger.Info("webhook skipped discussion", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) processJob(w http.ResponseWriter, kind, action string, job review.Job, dryReason string) {
	if s.reviewCfg.Target.DryRun {
		s.logger.Info("webhook skipped", "object_kind", kind, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "action", clip(action, 32), "reason", dryReason)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reason": "dry-run"})
		return
	}
	// GitLab's default webhook timeout is 10s and it does not retry 429/500.
	// Acknowledge first, then review in the background (one at a time).
	if !s.mu.TryLock() {
		s.skip(w, kind, job.ProjectID, job.MergeRequest, action, "busy")
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), s.jobTimeout())
		defer cancel()
		if err := s.runner.Process(ctx, job); err != nil && !errors.Is(err, review.ErrBlockingFindings) {
			s.logger.Error("webhook runner failed", "object_kind", kind, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "error", err)
			return
		}
		s.logger.Info("webhook processed", "object_kind", kind, "project_id", job.ProjectID, "mr_iid", job.MergeRequest, "action", clip(action, 32), "request_id", job.RequestID)
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
}

func (s *Server) skip(w http.ResponseWriter, kind string, projectID, iid int64, action, reason string) {
	s.logger.Info("webhook skipped", "object_kind", kind, "project_id", projectID, "mr_iid", iid, "action", clip(action, 32), "reason", reason)
	writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "reason": reason})
}

func (s *Server) rejectTooLarge(w http.ResponseWriter) {
	s.logger.Info("webhook rejected", "reason", "payload too large")
	w.Header().Set("Connection", "close")
	writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"status": "too_large"})
}

func (s *Server) isSelfUser(user gitlab.User) bool {
	if gitlab.IsAutomationUser(user) {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(user.Username))
	bot := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s.reviewCfg.GitLab.BotUsername, "@")))
	return bot != "" && name == bot
}

func (s *Server) mentionAliases() []string {
	aliases := s.reviewCfg.Review.MentionAliases
	if len(aliases) == 0 && s.reviewCfg.Review.Mention != "" {
		return []string{s.reviewCfg.Review.Mention}
	}
	return aliases
}

func (s *Server) jobTimeout() time.Duration {
	if s.reviewCfg.JobTimeout > 0 {
		return s.reviewCfg.JobTimeout
	}
	return 20 * time.Minute
}

func (s *Server) projectAllowed(ctx context.Context, projectID int64) bool {
	if projectID <= 0 {
		return false
	}
	ids, ok := s.memberProjectIDs(ctx)
	if !ok {
		return false
	}
	_, allowed := ids[projectID]
	return allowed
}

func (s *Server) memberProjectIDs(ctx context.Context) (map[int64]struct{}, bool) {
	if s.access == nil {
		return nil, false
	}
	s.memberMu.Lock()
	if inflight := s.memberRefresh; inflight != nil {
		s.memberMu.Unlock()
		return s.waitMemberRefresh(ctx, inflight)
	}
	inflight := &memberRefresh{done: make(chan struct{})}
	s.memberRefresh = inflight
	s.memberMu.Unlock()
	s.runMemberRefresh(inflight)
	return s.waitMemberRefresh(ctx, inflight)
}

func (s *Server) waitMemberRefresh(ctx context.Context, inflight *memberRefresh) (map[int64]struct{}, bool) {
	select {
	case <-inflight.done:
	case <-ctx.Done():
		s.memberMu.Lock()
		defer s.memberMu.Unlock()
		if s.memberLoaded {
			return s.memberIDs, true
		}
		return nil, false
	}
	s.memberMu.Lock()
	defer s.memberMu.Unlock()
	if !s.memberLoaded {
		return nil, false
	}
	return s.memberIDs, true
}

func (s *Server) runMemberRefresh(inflight *memberRefresh) {
	ctx, cancel := context.WithTimeout(context.Background(), s.membershipTimeout())
	ids, err := s.access.ListMemberProjectIDs(ctx, maxMemberProjects)
	cancel()

	s.memberMu.Lock()
	defer func() {
		s.memberRefresh = nil
		close(inflight.done)
		s.memberMu.Unlock()
	}()
	if err != nil {
		if s.memberLoaded {
			s.logger.Error("webhook membership refresh failed; using cached member projects", "error", err)
			return
		}
		s.logger.Error("webhook membership list failed", "error", err)
		return
	}
	if ids == nil {
		ids = map[int64]struct{}{}
	}
	if s.memberLoaded && memberIDsEqual(s.memberIDs, ids) {
		return
	}
	if s.memberLoaded {
		s.logger.Info("webhook membership cache updated", "projects", len(ids))
	}
	s.memberIDs = ids
	s.memberLoaded = true
}

func (s *Server) membershipTimeout() time.Duration {
	if s.reviewCfg.GitLab.Timeout > 0 {
		return s.reviewCfg.GitLab.Timeout
	}
	return defaultMembershipFetch
}

func memberIDsEqual(a, b map[int64]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}

func newRequestID(r *http.Request, projectID, iid int64) string {
	if id := strings.TrimSpace(r.Header.Get("X-Gitlab-Event-UUID")); id != "" {
		return clip(id, 128)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		return fmt.Sprintf("webhook-%d-%d-%x", projectID, iid, nonce[:])
	}
	return fmt.Sprintf("webhook-%d-%d-%d", projectID, iid, time.Now().UTC().UnixNano())
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizePath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = defaultPath
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func envOr(key, fallback string) string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	return strings.TrimSpace(raw)
}

func envBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func clip(value string, max int) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " ")
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}
