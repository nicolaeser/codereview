package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/review"
)

func TestSettingsValidateRequiresSecretOrSigningToken(t *testing.T) {
	err := Settings{Listen: defaultListen, Secret: "", SigningToken: "", MaxBody: defaultMaxBody, Path: defaultPath}.Validate()
	if err == nil {
		t.Fatal("missing secret and signing token must fail closed")
	}
	if err := (Settings{Listen: defaultListen, SigningToken: "whsec_abc", MaxBody: defaultMaxBody, Path: defaultPath}).Validate(); err != nil {
		t.Fatalf("signing token alone must be enough: %v", err)
	}
}

func TestLoadSettingsEmptySecret(t *testing.T) {
	clearWebhookEnv(t)
	t.Setenv("WEBHOOK_SECRET", "")
	t.Setenv("WEBHOOK_SIGNING_TOKEN", "")
	if _, err := LoadSettings(); err == nil {
		t.Fatal("LoadSettings must reject empty WEBHOOK_SECRET and WEBHOOK_SIGNING_TOKEN")
	}
}

func TestLoadSettingsDefaults(t *testing.T) {
	clearWebhookEnv(t)
	t.Setenv("WEBHOOK_SECRET", "unit-secret")
	settings, err := LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Listen != defaultListen {
		t.Fatalf("listen = %q, want %q", settings.Listen, defaultListen)
	}
	if strings.HasPrefix(settings.Listen, "0.0.0.0") || strings.HasPrefix(settings.Listen, ":") {
		t.Fatalf("listen must default to loopback, got %q", settings.Listen)
	}
	if settings.MaxBody != defaultMaxBody {
		t.Fatalf("max body = %d", settings.MaxBody)
	}
	if settings.Path != defaultPath {
		t.Fatalf("path = %q", settings.Path)
	}
	if !settings.HandleMR || !settings.HandleNotes {
		t.Fatal("MR and note handling must default true")
	}
}

func TestWebhookAcceptsSigningToken(t *testing.T) {
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("signing-secret"))
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{signingToken: token})
	body := mergeRequestJSON("open")
	id := "msg-1"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rec := postWebhook(t, srv, "", body, http.Header{
		webhookIDHeader:        {id},
		webhookTimestampHeader: {ts},
		webhookSignatureHeader: {signWebhook(t, token, id, ts, body)},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 1 {
		t.Fatalf("jobs = %d, want 1", len(runner.jobs()))
	}
}

func TestWebhookSigningTokenRejectsStaleTimestamp(t *testing.T) {
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("signing-secret"))
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{signingToken: token})
	body := mergeRequestJSON("open")
	id := "msg-old"
	ts := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	rec := postWebhook(t, srv, "", body, http.Header{
		webhookIDHeader:        {id},
		webhookTimestampHeader: {ts},
		webhookSignatureHeader: {signWebhook(t, token, id, ts, body)},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestWebhookSigningTokenRejectsBadSignature(t *testing.T) {
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("signing-secret"))
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{signingToken: token})
	body := mergeRequestJSON("open")
	rec := postWebhook(t, srv, "", body, http.Header{
		webhookIDHeader:        {"msg-1"},
		webhookTimestampHeader: {strconv.FormatInt(time.Now().Unix(), 10)},
		webhookSignatureHeader: {"v1,AAAA"},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("bad signature must not call Process")
	}
}

func TestWebhookFallsBackToSecretWhenNoSignature(t *testing.T) {
	token := "whsec_" + base64.StdEncoding.EncodeToString([]byte("signing-secret"))
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{secret: "secret", signingToken: token})
	rec := postWebhook(t, srv, "secret", mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 1 {
		t.Fatalf("jobs = %d, want 1", len(runner.jobs()))
	}
}

func TestSecretMatches(t *testing.T) {
	if secretMatches("secret", "") {
		t.Fatal("empty configured secret must never match")
	}
	if secretMatches("", "secret") {
		t.Fatal("empty provided token must not match")
	}
	if !secretMatches("secret", "secret") {
		t.Fatal("equal tokens must match")
	}
	if secretMatches("secret", "Secret") {
		t.Fatal("token compare is case-sensitive")
	}
}

func TestHealth(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want ok", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("health must not call Process")
	}
}

func TestWebhookMissingToken(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "", mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 {
		t.Fatal("Process must not be called without a token")
	}
}

func TestWebhookWrongToken(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "wrong", mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("Process must not be called with a wrong token")
	}
}

func TestWebhookMergeRequestOpen(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	headers := http.Header{"X-Gitlab-Event-UUID": []string{"evt-open-1"}}
	rec := postWebhook(t, srv, "secret", mergeRequestJSON("open"), headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	jobs := runner.jobs()
	if len(jobs) != 1 {
		t.Fatalf("Process calls = %d, want 1", len(jobs))
	}
	job := jobs[0]
	if job.ProjectID != 1 || job.MergeRequest != 1 {
		t.Fatalf("job identity = project %d mr %d", job.ProjectID, job.MergeRequest)
	}
	if job.Force {
		t.Fatal("Force must be false so SHA skip can dedup CI+webhook")
	}
	if job.Full {
		t.Fatal("Full must be false for merge_request events")
	}
	if job.Reason != "GitLab webhook" {
		t.Fatalf("reason = %q", job.Reason)
	}
	if job.RequestID != "evt-open-1" {
		t.Fatalf("request id = %q", job.RequestID)
	}
}

func TestWebhookMergeRequestMergeAndClose(t *testing.T) {
	for _, action := range []string{"merge", "close"} {
		t.Run(action, func(t *testing.T) {
			runner := &spyRunner{}
			srv := newTestServer(t, runner, testOpts{})
			rec := postWebhook(t, srv, "secret", mergeRequestJSON(action), nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "skipped") {
				t.Fatalf("body = %s", rec.Body.String())
			}
			if len(runner.jobs()) != 0 {
				t.Fatalf("Process called for action %s", action)
			}
		})
	}
}

func TestWebhookNoteWithoutMention(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("looks good", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 {
		t.Fatal("note without mention must not review or skip a discussion")
	}
}

func TestWebhookNoteSkipCommand(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview skip", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("skip command must not call Process")
	}
	skips := runner.skipCalls()
	if len(skips) != 1 {
		t.Fatalf("SkipDiscussion calls = %d, want 1", len(skips))
	}
	got := skips[0]
	if got.projectID != 1 || got.iid != 1 || got.discussionID != "abc" {
		t.Fatalf("skip call = %#v", got)
	}
	if got.noteBody != "@codereview skip" || got.author != "root" {
		t.Fatalf("skip call = %#v", got)
	}
}

func TestWebhookBareMentionPostsHelp(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("bare mention must not call Process")
	}
	if len(runner.skipCalls()) != 0 {
		t.Fatal("bare mention must not SkipDiscussion")
	}
	helps := runner.helpCalls()
	if len(helps) != 1 {
		t.Fatalf("ReplyHelp calls = %d, want 1", len(helps))
	}
	if helps[0].projectID != 1 || helps[0].iid != 1 || helps[0].discussionID != "abc" {
		t.Fatalf("help call = %#v", helps[0])
	}
	if helps[0].mention != "codereview" {
		t.Fatalf("mention = %q", helps[0].mention)
	}
}

func TestWebhookNotePleaseReview(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview please review", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.skipCalls()) != 0 {
		t.Fatal("review mention must not SkipDiscussion")
	}
	jobs := runner.jobs()
	if len(jobs) != 1 {
		t.Fatalf("Process calls = %d, want 1", len(jobs))
	}
	job := jobs[0]
	if !job.Force || job.Full {
		t.Fatalf("mention job Force=%t Full=%t, want force incremental", job.Force, job.Full)
	}
	if job.ProjectID != 1 || job.MergeRequest != 1 {
		t.Fatalf("job identity = project %d mr %d", job.ProjectID, job.MergeRequest)
	}
	if job.Reason != "GitLab mention webhook" {
		t.Fatalf("reason = %q", job.Reason)
	}
}

func TestWebhookReviewKeepsExtraFocus(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview review also check authz", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	jobs := runner.jobs()
	if len(jobs) != 1 || jobs[0].Ask || jobs[0].ExtraFocus != "also check authz" || jobs[0].Full {
		t.Fatalf("jobs=%#v", jobs)
	}
}

func TestWebhookAskDoesNotFullReview(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview ask why is line 40 using atoi", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	jobs := runner.jobs()
	if len(jobs) != 1 || !jobs[0].Ask || jobs[0].ExtraFocus != "why is line 40 using atoi" || jobs[0].DiscussionID != "abc" {
		t.Fatalf("ask job = %#v", jobs)
	}
}

func TestWebhookFullReviewHyphenAndCompact(t *testing.T) {
	for _, body := range []string{"@codereview full-review", "@codereview fullreview"} {
		runner := &spyRunner{}
		srv := newTestServer(t, runner, testOpts{})
		rec := postWebhook(t, srv, "secret", noteJSON(body, "MergeRequest", "abc"), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", body, rec.Code, rec.Body.String())
		}
		srv.waitIdle()
		jobs := runner.jobs()
		if len(jobs) != 1 || !jobs[0].Full || !jobs[0].Force {
			t.Fatalf("%s jobs=%#v, want one full force review", body, jobs)
		}
	}
}

func TestWebhookFullReviewSetsFull(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview full review", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	jobs := runner.jobs()
	if len(jobs) != 1 || !jobs[0].Full || !jobs[0].Force {
		t.Fatalf("jobs=%#v, want one full force review", jobs)
	}
}

func TestWebhookResolveOwned(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview resolve", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("resolve must not call Process")
	}
	if runner.resolveCalls() != 1 {
		t.Fatalf("ResolveOwned calls = %d, want 1", runner.resolveCalls())
	}
}

func TestWebhookNoteOnIssue(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	rec := postWebhook(t, srv, "secret", noteJSON("@codereview please review", "Issue", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 {
		t.Fatal("issue notes must be skipped")
	}
}

func TestWebhookBotAuthor(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	body := `{"object_kind":"merge_request","user":{"username":"project_1_bot_deadbeef"},"project":{"id":1},"object_attributes":{"iid":1,"action":"open","state":"opened"}}`
	rec := postWebhook(t, srv, "secret", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 {
		t.Fatal("automation user events must be skipped")
	}
}

func TestWebhookSelfUsernameSkipped(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	body := `{"object_kind":"note","user":{"username":"codereview"},"project":{"id":1},"object_attributes":{"note":"@codereview please review","noteable_type":"MergeRequest","discussion_id":"abc","action":"create"},"merge_request":{"iid":1,"id":10}}`
	rec := postWebhook(t, srv, "secret", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 {
		t.Fatal("notes from GITLAB_BOT_USERNAME must be skipped to prevent loops")
	}
}

func TestWebhookSystemNoteSkipped(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	body := `{"object_kind":"note","user":{"username":"root"},"project":{"id":1},"object_attributes":{"note":"@codereview please review","noteable_type":"MergeRequest","discussion_id":"abc","action":"create","system":true},"merge_request":{"iid":1,"id":10}}`
	rec := postWebhook(t, srv, "secret", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 {
		t.Fatal("system notes must be skipped")
	}
}

func TestWebhookBodyTooLarge(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{maxBody: 32})
	req := httptest.NewRequest(http.MethodPost, "/gitlab-webhook", strings.NewReader(mergeRequestJSON("open")))
	req.Header.Set("X-Gitlab-Token", "secret")
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = 64
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("oversized body must not call Process")
	}

	large := `{"object_kind":"merge_request","pad":"` + strings.Repeat("a", 80) + `"}`
	rec = postWebhook(t, srv, "secret", large, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookDryRunDoesNotCallRunner(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{dryRun: true})
	rec := postWebhook(t, srv, "secret", mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = postWebhook(t, srv, "secret", noteJSON("@codereview skip", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("skip dry-run status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = postWebhook(t, srv, "secret", noteJSON("@codereview please review", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("review dry-run status = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = postWebhook(t, srv, "secret", noteJSON("@codereview", "MergeRequest", "abc"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("help dry-run status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 || len(runner.helpCalls()) != 0 {
		t.Fatal("dry-run must not call Process, SkipDiscussion, or ReplyHelp")
	}
}

func TestWebhookUnknownObjectKind(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	rec := postWebhook(t, srv, "secret", `{"object_kind":"push","project":{"id":99}}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown object_kind") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("unknown object_kind must not call Process")
	}
	if access.callCount() != 0 {
		t.Fatal("push events must not list member projects")
	}
}

func TestWebhookSkipsNonMemberMergeRequestWithoutRunner(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	rec := postWebhook(t, srv, "secret", mergeRequestJSONFor("open", 99, 4), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a project member") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 {
		t.Fatal("non-member merge requests must not call Process")
	}
	if access.callCount() != 1 {
		t.Fatalf("membership list calls = %d, want 1", access.callCount())
	}
}

func TestWebhookSkipsNonMemberMentionWithoutGitLabWrites(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	for _, note := range []string{"@codereview please review", "@codereview skip", "@codereview"} {
		rec := postWebhook(t, srv, "secret", noteJSONFor(note, "MergeRequest", "abc", 99, 4), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "not a project member") {
			t.Fatalf("body = %s", rec.Body.String())
		}
	}
	if len(runner.jobs()) != 0 || len(runner.skipCalls()) != 0 || len(runner.helpCalls()) != 0 || runner.resolveCalls() != 0 {
		t.Fatal("non-member mentions must not call the runner")
	}
}

func TestWebhookNoteWithoutMentionDoesNotListMembers(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	rec := postWebhook(t, srv, "secret", noteJSONFor("looks fine", "MergeRequest", "abc", 99, 4), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if access.callCount() != 0 {
		t.Fatal("notes without a bot mention must not list member projects")
	}
	if len(runner.jobs()) != 0 {
		t.Fatal("unmentioned notes must not call Process")
	}
}

func TestWebhookMembershipLiveRefreshWhenUnchanged(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	if rec := postWebhook(t, srv, "secret", mergeRequestJSONFor("open", 99, 1), nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := postWebhook(t, srv, "secret", mergeRequestJSONFor("update", 99, 1), nil); rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if access.callCount() != 2 {
		t.Fatalf("membership list calls = %d, want 2 live refreshes", access.callCount())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 {
		t.Fatal("unchanged non-member cache must keep skipping")
	}
}

func TestWebhookMembershipLiveChangeApplies(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	rec := postWebhook(t, srv, "secret", mergeRequestJSONFor("open", 99, 4), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a project member") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	access.set(map[int64]struct{}{1: {}, 99: {}}, nil)
	rec = postWebhook(t, srv, "secret", mergeRequestJSONFor("update", 99, 4), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 1 {
		t.Fatalf("Process calls = %d, want 1 after live membership added project 99", len(runner.jobs()))
	}
}

func TestWebhookMembershipLiveRefreshCoalesces(t *testing.T) {
	access := &countingProjectAccess{
		ids:     map[int64]struct{}{1: {}},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	srv := newTestServer(t, &spyRunner{}, testOpts{access: access})
	entered := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			entered <- struct{}{}
			if _, ok := srv.memberProjectIDs(context.Background()); !ok {
				t.Error("expected membership snapshot")
			}
		}()
	}
	<-entered
	<-entered
	select {
	case <-access.started:
	case <-time.After(2 * time.Second):
		t.Fatal("live membership list did not start")
	}
	close(access.release)
	wg.Wait()
	if access.callCount() != 1 {
		t.Fatalf("membership list calls = %d, want 1 coalesced live refresh", access.callCount())
	}
}

func TestWebhookMembershipListErrorSkipsUntilLoaded(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{err: errors.New("gitlab unavailable")}
	srv := newTestServer(t, runner, testOpts{access: access})
	rec := postWebhook(t, srv, "secret", mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not a project member") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 0 {
		t.Fatal("membership list failure must not start a review")
	}
}

func TestWebhookMembershipRefreshFailureKeepsCachedAllow(t *testing.T) {
	runner := &spyRunner{}
	access := &countingProjectAccess{ids: map[int64]struct{}{1: {}}}
	srv := newTestServer(t, runner, testOpts{access: access})
	if rec := postWebhook(t, srv, "secret", mergeRequestJSON("open"), nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	access.set(map[int64]struct{}{1: {}}, errors.New("gitlab unavailable"))
	if rec := postWebhook(t, srv, "secret", mergeRequestJSON("update"), nil); rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 2 {
		t.Fatalf("Process calls = %d, want 2 using stale membership cache", len(runner.jobs()))
	}
}

func TestWebhookNoteEventTypeWithoutObjectKind(t *testing.T) {
	runner := &spyRunner{}
	srv := newTestServer(t, runner, testOpts{})
	body := `{"event_type":"note","user":{"username":"root"},"project":{"id":1},"object_attributes":{"note":"@codereview please review","noteable_type":"MergeRequest","discussion_id":"abc"},"merge_request":{"iid":1,"id":10}}`
	rec := postWebhook(t, srv, "secret", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	srv.waitIdle()
	if len(runner.jobs()) != 1 {
		t.Fatalf("Process calls = %d, want 1", len(runner.jobs()))
	}
}

func TestWebhookBusyReturns200(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &spyRunner{processHook: func() {
		close(started)
		<-release
	}}
	srv := newTestServer(t, runner, testOpts{})
	first := postWebhook(t, srv, "secret", mergeRequestJSON("open"), nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", first.Code, first.Body.String())
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first Process did not start")
	}
	rec := postWebhook(t, srv, "secret", mergeRequestJSON("update"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "busy") {
		t.Fatalf("body = %s, want busy skip", rec.Body.String())
	}
	close(release)
	srv.waitIdle()
}

func TestWebhookLogsOmitSecretAndBody(t *testing.T) {
	var buf bytes.Buffer
	runner := &spyRunner{}
	token := "super-secret-webhook-token"
	srv := newTestServer(t, runner, testOpts{logger: logx.New(&buf, "debug"), secret: token})
	rec := postWebhook(t, srv, token, mergeRequestJSON("open"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	srv.waitIdle()
	logs := buf.String()
	if strings.Contains(logs, token) {
		t.Fatal("logs contained the webhook token")
	}
	if strings.Contains(logs, "X-Gitlab-Token") || strings.Contains(logs, "Authorization") {
		t.Fatal("logs mentioned auth headers")
	}
}

func TestServerTimeouts(t *testing.T) {
	srv := newTestServer(t, &spyRunner{}, testOpts{})
	if srv.httpServer.ReadHeaderTimeout != 5*time.Second || srv.httpServer.ReadTimeout != 15*time.Second || srv.httpServer.IdleTimeout != 60*time.Second {
		t.Fatalf("timeouts = header %s read %s idle %s", srv.httpServer.ReadHeaderTimeout, srv.httpServer.ReadTimeout, srv.httpServer.IdleTimeout)
	}
	if srv.httpServer.Addr != defaultListen {
		t.Fatalf("addr = %q", srv.httpServer.Addr)
	}
}

type skipCall struct {
	projectID    int64
	iid          int64
	discussionID string
	noteBody     string
	author       string
}

type helpCall struct {
	projectID    int64
	iid          int64
	discussionID string
	mention      string
}

type spyRunner struct {
	mu          sync.Mutex
	process     []review.Job
	skips       []skipCall
	helps       []helpCall
	resolves    int
	processErr  error
	skipErr     error
	processHook func()
}

func (s *spyRunner) Process(ctx context.Context, job review.Job) error {
	if s.processHook != nil {
		s.processHook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.process = append(s.process, job)
	return s.processErr
}

func (s *spyRunner) SkipDiscussion(ctx context.Context, projectID, iid int64, discussionID, noteBody, author string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skips = append(s.skips, skipCall{projectID: projectID, iid: iid, discussionID: discussionID, noteBody: noteBody, author: author})
	return s.skipErr
}

func (s *spyRunner) ReplyHelp(ctx context.Context, projectID, iid int64, discussionID, mention string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.helps = append(s.helps, helpCall{projectID: projectID, iid: iid, discussionID: discussionID, mention: mention})
	return nil
}

func (s *spyRunner) ResolveOwned(ctx context.Context, projectID, iid int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolves++
	return nil
}

func (s *spyRunner) resolveCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolves
}

func (s *spyRunner) helpCalls() []helpCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]helpCall, len(s.helps))
	copy(out, s.helps)
	return out
}

func (s *spyRunner) jobs() []review.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]review.Job, len(s.process))
	copy(out, s.process)
	return out
}

func (s *spyRunner) skipCalls() []skipCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]skipCall, len(s.skips))
	copy(out, s.skips)
	return out
}

type testOpts struct {
	dryRun       bool
	maxBody      int64
	secret       string
	signingToken string
	logger       *logx.Logger
	access       ProjectAccess
}

type staticProjectAccess map[int64]struct{}

func (s staticProjectAccess) ListMemberProjectIDs(context.Context, int) (map[int64]struct{}, error) {
	return s, nil
}

type countingProjectAccess struct {
	mu      sync.Mutex
	once    sync.Once
	ids     map[int64]struct{}
	err     error
	calls   int
	started chan struct{}
	release chan struct{}
}

func (c *countingProjectAccess) ListMemberProjectIDs(ctx context.Context, _ int) (map[int64]struct{}, error) {
	c.mu.Lock()
	c.calls++
	ids := c.ids
	err := c.err
	started := c.started
	release := c.release
	c.mu.Unlock()
	c.once.Do(func() {
		if started != nil {
			close(started)
		}
	})
	if release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
	}
	if err != nil {
		return nil, err
	}
	out := make(map[int64]struct{}, len(ids))
	for id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}

func (c *countingProjectAccess) set(ids map[int64]struct{}, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = ids
	c.err = err
}

func (c *countingProjectAccess) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newTestServer(t *testing.T, runner *spyRunner, opts testOpts) *Server {
	t.Helper()
	maxBody := opts.maxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}
	logger := opts.logger
	if logger == nil {
		logger = logx.New(io.Discard, "error")
	}
	cfg := config.Config{
		JobTimeout: time.Minute,
		GitLab:     config.GitLabConfig{BotUsername: "codereview", Timeout: 2 * time.Second},
		Review: config.ReviewConfig{
			Mention:        "codereview",
			MentionAliases: []string{"codereview"},
		},
		Target: config.TargetConfig{DryRun: opts.dryRun},
	}
	secret := opts.secret
	if secret == "" && opts.signingToken == "" {
		secret = "secret"
	}
	settings := Settings{
		Listen:       defaultListen,
		Secret:       secret,
		SigningToken: opts.signingToken,
		MaxBody:      maxBody,
		Path:         defaultPath,
		HandleMR:     true,
		HandleNotes:  true,
	}
	access := opts.access
	if access == nil {
		access = staticProjectAccess{1: {}}
	}
	return New(cfg, settings, runner, access, logger)
}

func postWebhook(t *testing.T, srv *Server, token, body string, extra http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/gitlab-webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Gitlab-Token", token)
	}
	for key, values := range extra {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	return rec
}

func signWebhook(t *testing.T, token, id, ts, body string) string {
	t.Helper()
	key, err := decodeSigningKey(token)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + ts + "." + body))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func mergeRequestJSON(action string) string {
	return mergeRequestJSONFor(action, 1, 1)
}

func mergeRequestJSONFor(action string, projectID, iid int64) string {
	payload := map[string]any{
		"object_kind": "merge_request",
		"user":        map[string]any{"username": "root", "bot": false},
		"project":     map[string]any{"id": projectID},
		"object_attributes": map[string]any{
			"iid":    iid,
			"action": action,
			"state":  "opened",
		},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func noteJSON(note, noteableType, discussionID string) string {
	return noteJSONFor(note, noteableType, discussionID, 1, 1)
}

func noteJSONFor(note, noteableType, discussionID string, projectID, iid int64) string {
	payload := map[string]any{
		"object_kind": "note",
		"user":        map[string]any{"username": "root"},
		"project":     map[string]any{"id": projectID},
		"object_attributes": map[string]any{
			"note":          note,
			"noteable_type": noteableType,
			"discussion_id": discussionID,
			"action":        "create",
		},
		"merge_request": map[string]any{"iid": iid, "id": 10},
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func clearWebhookEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"WEBHOOK_LISTEN", "WEBHOOK_SECRET", "WEBHOOK_SIGNING_TOKEN", "WEBHOOK_MAX_BODY", "WEBHOOK_PATH",
		"WEBHOOK_HANDLE_MR", "WEBHOOK_HANDLE_NOTES",
	} {
		t.Setenv(key, "")
	}
}
