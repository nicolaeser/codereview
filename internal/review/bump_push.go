package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

const maxBumpPushReportBytes = 256 << 10

type bumpPushPayload struct {
	Repo                   string   `json:"repo"`
	ProjectID              int64    `json:"project_id"`
	MergeRequestIID        int64    `json:"merge_request_iid"`
	MergeRequestURL        string   `json:"merge_request_url,omitempty"`
	Title                  string   `json:"title,omitempty"`
	Author                 string   `json:"author,omitempty"`
	SourceBranch           string   `json:"source_branch"`
	TargetBranch           string   `json:"target_branch"`
	HeadSHA                string   `json:"head_sha"`
	SuggestedBranch        string   `json:"suggested_branch,omitempty"`
	DependencyFiles        []string `json:"dependency_files,omitempty"`
	CodeAdaptationRequired bool     `json:"code_adaptation_required"`
	Report                 string   `json:"report"`
	Summary                string   `json:"summary,omitempty"`
}

func (s *Service) pushClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func mergeRequestRepo(mr gitlab.MergeRequest) string {
	full := strings.TrimSpace(mr.References.Full)
	if i := strings.LastIndex(full, "!"); i > 0 {
		return full[:i]
	}
	web := strings.TrimSpace(mr.WebURL)
	if i := strings.Index(web, "/-/merge_requests/"); i > 0 {
		rest := web[:i]
		if j := strings.Index(rest, "://"); j >= 0 {
			rest = rest[j+3:]
			if k := strings.Index(rest, "/"); k >= 0 {
				return rest[k+1:]
			}
		}
	}
	return ""
}

func (s *Service) postBumpPush(ctx context.Context, mr gitlab.MergeRequest, headSHA string, prepared []PreparedDiff, result AIReview) error {
	url := strings.TrimSpace(s.cfg.Review.BumpPushURL)
	if url == "" {
		return fmt.Errorf("dependency bump PUSH is enabled but DEPENDENCY_BUMP_PUSH_URL is empty")
	}
	report := strings.TrimSpace(result.AgentReport)
	if report == "" {
		return fmt.Errorf("dependency bump PUSH requires a non-empty report")
	}
	if len(report) > maxBumpPushReportBytes {
		report = report[:maxBumpPushReportBytes] + "\n[truncated by CodeReview]"
	}
	adaptation := result.CodeAdaptationRequired != nil && *result.CodeAdaptationRequired
	payload := bumpPushPayload{
		Repo:                   mergeRequestRepo(mr),
		ProjectID:              mr.ProjectID,
		MergeRequestIID:        mr.IID,
		MergeRequestURL:        mr.WebURL,
		Title:                  mr.Title,
		Author:                 mr.Author.Username,
		SourceBranch:           mr.SourceBranch,
		TargetBranch:           mr.TargetBranch,
		HeadSHA:                headSHA,
		SuggestedBranch:        strings.TrimSpace(result.BranchName),
		DependencyFiles:        dependencyPaths(prepared),
		CodeAdaptationRequired: adaptation,
		Report:                 report,
		Summary:                strings.TrimSpace(result.Summary),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode dependency bump PUSH payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("dependency bump PUSH request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret := strings.TrimSpace(s.cfg.Review.BumpPushSecret); secret != "" {
		req.Header.Set("X-CodeReview-Token", secret)
	}
	resp, err := s.pushClient().Do(req)
	if err != nil {
		return fmt.Errorf("dependency bump PUSH: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("dependency bump PUSH: unexpected status %s", resp.Status)
	}
	return nil
}
