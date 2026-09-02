package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes      = 20 * 1024 * 1024
	maxRedirectHops       = 5
	maxDiscussionPages    = 5 // a few hundred notes; a busy MR must not paginate forever
	maxNotePages          = 5 // 500 notes; a busy MR must not paginate forever
	maxMemberProjectPages = 5 // 500 projects; instance hooks must not paginate forever
	memberProjectsPerPage = 100
)

type Client struct {
	baseURL  string
	token    string
	jobToken bool
	retries  int
	http     *http.Client
}

type APIError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("GitLab %s %s returned HTTP %d", e.Method, e.URL, e.Status)
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		msg += "; check that the token can read this project and write MR notes/discussions"
	}
	return msg
}

func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// New creates a GitLab API client using the PRIVATE-TOKEN header.
func New(apiURL, token string, timeout time.Duration) *Client {
	return NewWithOptions(apiURL, token, timeout, false, 2)
}

// NewWithOptions creates a GitLab API client. jobToken selects JOB-TOKEN; maxRetries is extra attempts after the first.
func NewWithOptions(apiURL, token string, timeout time.Duration, jobToken bool, maxRetries int) *Client {
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &Client{
		baseURL:  strings.TrimRight(apiURL, "/"),
		token:    token,
		jobToken: jobToken,
		retries:  maxRetries,
		http: &http.Client{
			Timeout:       timeout,
			CheckRedirect: redirectWithoutCrossHostAuth(),
		},
	}
}

func (c *Client) GetMergeRequest(ctx context.Context, projectID, iid int64) (MergeRequest, error) {
	var result MergeRequest
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d", projectID, iid), nil, nil, &result)
	return result, err
}

func (c *Client) ListMergeRequestDiffs(ctx context.Context, projectID, iid int64, maxFiles int) ([]Diff, error) {
	var result []Diff
	for page := 1; len(result) < maxFiles; page++ {
		var batch []Diff
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"100"}, "unidiff": {"true"}}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d/diffs", projectID, iid), query, nil, &batch); err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < 100 {
			break
		}
	}
	if len(result) > maxFiles {
		result = result[:maxFiles]
	}
	return result, nil
}

func (c *Client) Compare(ctx context.Context, projectID int64, from, to string) ([]Diff, error) {
	query := url.Values{"from": {from}, "to": {to}, "straight": {"true"}}
	var result CompareResult
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/repository/compare", projectID), query, nil, &result)
	return result.Diffs, err
}

func (c *Client) LatestDiffVersion(ctx context.Context, projectID, iid int64) (DiffVersion, error) {
	var versions []DiffVersion
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d/versions", projectID, iid), nil, nil, &versions)
	if err != nil {
		return DiffVersion{}, err
	}
	if len(versions) == 0 {
		return DiffVersion{}, errors.New("merge request has no diff versions")
	}
	return versions[0], nil
}

func (c *Client) GetFile(ctx context.Context, projectID int64, path, ref string) (string, error) {
	query := url.Values{"ref": {ref}}
	target := fmt.Sprintf("/projects/%d/repository/files/%s/raw", projectID, url.PathEscape(path))
	raw, err := c.doRaw(ctx, http.MethodGet, target, query, nil)
	return string(raw), err
}

func (c *Client) ListTree(ctx context.Context, projectID int64, ref string, maxEntries int) ([]TreeEntry, error) {
	var result []TreeEntry
	for page := 1; len(result) < maxEntries; page++ {
		var batch []TreeEntry
		query := url.Values{"ref": {ref}, "recursive": {"true"}, "page": {strconv.Itoa(page)}, "per_page": {"100"}}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/repository/tree", projectID), query, nil, &batch); err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < 100 {
			break
		}
	}
	if len(result) > maxEntries {
		result = result[:maxEntries]
	}
	return result, nil
}

func (c *Client) GetIssue(ctx context.Context, projectID, iid int64) (Issue, error) {
	var result Issue
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/issues/%d", projectID, iid), nil, nil, &result)
	return result, err
}

func (c *Client) CreateNote(ctx context.Context, projectID, iid int64, body string) (Note, error) {
	var result Note
	payload := map[string]any{"body": body}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/merge_requests/%d/notes", projectID, iid), nil, payload, &result)
	return result, err
}

func (c *Client) UpdateNote(ctx context.Context, projectID, iid, noteID int64, body string) (Note, error) {
	var result Note
	payload := map[string]any{"body": body}
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%d/merge_requests/%d/notes/%d", projectID, iid, noteID), nil, payload, &result)
	return result, err
}

func (c *Client) ListMergeRequestNotes(ctx context.Context, projectID, iid int64) ([]Note, error) {
	const perPage = 100
	var result []Note
	for page := 1; page <= maxNotePages; page++ {
		var batch []Note
		query := url.Values{
			"page":     {strconv.Itoa(page)},
			"per_page": {strconv.Itoa(perPage)},
			"sort":     {"desc"},
			"order_by": {"created_at"},
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d/notes", projectID, iid), query, nil, &batch); err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return result, nil
}

func (c *Client) UpdateMergeRequestDescription(ctx context.Context, projectID, iid int64, description string) error {
	payload := map[string]any{"description": description}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%d/merge_requests/%d", projectID, iid), nil, payload, nil)
}

func (c *Client) CreateDiffDiscussion(ctx context.Context, projectID, iid int64, body string, position DiffPosition) (Discussion, error) {
	var result Discussion
	payload := map[string]any{"body": body, "position": position}
	err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/merge_requests/%d/discussions", projectID, iid), nil, payload, &result)
	return result, err
}

func (c *Client) ReplyToDiscussion(ctx context.Context, projectID, iid int64, discussionID, body string) (Note, error) {
	var result Note
	payload := map[string]any{"body": body}
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s/notes", projectID, iid, url.PathEscape(discussionID))
	err := c.do(ctx, http.MethodPost, path, nil, payload, &result)
	return result, err
}

func (c *Client) ListDiscussions(ctx context.Context, projectID, iid int64) ([]Discussion, error) {
	const perPage = 100
	var result []Discussion
	for page := 1; page <= maxDiscussionPages; page++ {
		var batch []Discussion
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d/discussions", projectID, iid), query, nil, &batch); err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return result, nil
}

func (c *Client) ResolveDiscussion(ctx context.Context, projectID, iid int64, discussionID string) error {
	payload := map[string]any{"resolved": true}
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s", projectID, iid, url.PathEscape(discussionID))
	return c.do(ctx, http.MethodPut, path, nil, payload, nil)
}

func (c *Client) SetCommitStatus(ctx context.Context, projectID int64, sha, state, name, description, targetURL string) error {
	payload := map[string]any{"state": state, "name": name, "description": description, "target_url": targetURL}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/statuses/%s", projectID, url.PathEscape(sha)), nil, payload, nil)
}

func (c *Client) GetCurrentUser(ctx context.Context) (User, error) {
	var result User
	err := c.do(ctx, http.MethodGet, "/user", nil, nil, &result)
	return result, err
}

func (c *Client) ListMemberProjectIDs(ctx context.Context, maxProjects int) (map[int64]struct{}, error) {
	if maxProjects <= 0 {
		maxProjects = maxMemberProjectPages * memberProjectsPerPage
	}
	limit := maxMemberProjectPages * memberProjectsPerPage
	if maxProjects < limit {
		limit = maxProjects
	}
	out := make(map[int64]struct{}, limit)
	for page := 1; page <= maxMemberProjectPages && len(out) < limit; page++ {
		var batch []struct {
			ID int64 `json:"id"`
		}
		query := url.Values{
			"membership": {"true"},
			"simple":     {"true"},
			"page":       {strconv.Itoa(page)},
			"per_page":   {strconv.Itoa(memberProjectsPerPage)},
		}
		if err := c.do(ctx, http.MethodGet, "/projects", query, nil, &batch); err != nil {
			return nil, err
		}
		for _, project := range batch {
			if project.ID <= 0 {
				continue
			}
			out[project.ID] = struct{}{}
			if len(out) >= limit {
				break
			}
		}
		if len(batch) < memberProjectsPerPage {
			break
		}
	}
	return out, nil
}

func (c *Client) GetMergeRequestApprovals(ctx context.Context, projectID, iid int64) (MergeRequestApprovals, error) {
	var result MergeRequestApprovals
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/merge_requests/%d/approvals", projectID, iid), nil, nil, &result)
	return result, err
}

func (c *Client) ApproveMergeRequest(ctx context.Context, projectID, iid int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/merge_requests/%d/approve", projectID, iid), nil, map[string]any{}, nil)
}

func (c *Client) UnapproveMergeRequest(ctx context.Context, projectID, iid int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/merge_requests/%d/unapprove", projectID, iid), nil, map[string]any{}, nil)
}

func (c *Client) SetMergeRequestReviewers(ctx context.Context, projectID, iid int64, reviewerIDs []int64) error {
	payload := map[string]any{"reviewer_ids": reviewerIDs}
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/projects/%d/merge_requests/%d", projectID, iid), nil, payload, nil)
}

func (c *Client) ListTodos(ctx context.Context, maxItems int) ([]Todo, error) {
	if maxItems <= 0 {
		maxItems = 20
	}
	const perPage = 20
	var result []Todo
	for page := 1; len(result) < maxItems; page++ {
		var batch []Todo
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}, "state": {"pending"}}
		if err := c.do(ctx, http.MethodGet, "/todos", query, nil, &batch); err != nil {
			return nil, err
		}
		result = append(result, batch...)
		if len(batch) < perPage {
			break
		}
	}
	if len(result) > maxItems {
		result = result[:maxItems]
	}
	return result, nil
}

func (c *Client) MarkTodoDone(ctx context.Context, todoID int64) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/todos/%d/mark_as_done", todoID), nil, map[string]any{}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, payload any, target any) error {
	raw, err := c.doRaw(ctx, method, path, query, payload)
	if err != nil {
		return err
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode GitLab response: %w", err)
	}
	return nil
}

func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, payload any) ([]byte, error) {
	target := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var encoded []byte
	if payload != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(payload); err != nil {
			return nil, err
		}
		encoded = buf.Bytes()
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		raw, retryAfter, hasRetryAfter, retryable, err := c.attempt(ctx, method, target, encoded)
		if err == nil {
			return raw, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryable || attempt == c.retries {
			return nil, err
		}
		if err := waitRetry(ctx, retryDelay(attempt, retryAfter, hasRetryAfter)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) attempt(ctx context.Context, method, target string, encoded []byte) ([]byte, time.Duration, bool, bool, error) {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, 0, false, false, err
	}
	if c.jobToken {
		request.Header.Set("JOB-TOKEN", c.token)
	} else {
		request.Header.Set("PRIVATE-TOKEN", c.token)
	}
	request.Header.Set("Accept", "application/json")
	if encoded != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, false, true, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, false, false, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return raw, 0, false, false, nil
	}
	delay, hasRetryAfter := parseRetryAfter(response)
	return nil, delay, hasRetryAfter, retryableStatus(method, response.StatusCode), &APIError{
		Method: method,
		URL:    target,
		Status: response.StatusCode,
		Body:   string(raw),
	}
}

func retryableStatus(method string, status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	switch method {
	case http.MethodGet, http.MethodHead:
		return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
	default:
		return false
	}
}

func retryDelay(attempt int, retryAfter time.Duration, hasRetryAfter bool) time.Duration {
	if hasRetryAfter {
		if retryAfter < 0 {
			return 0
		}
		return retryAfter
	}
	return time.Duration(math.Pow(2, float64(attempt))) * time.Second
}

func parseRetryAfter(response *http.Response) (time.Duration, bool) {
	raw := response.Header.Get("Retry-After")
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(raw); err == nil {
		return time.Until(at), true
	}
	return 0, false
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func redirectWithoutCrossHostAuth(extraAuthHeaders ...string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirectHops {
			return fmt.Errorf("stopped after %d redirects", maxRedirectHops)
		}
		if len(via) == 0 {
			return nil
		}
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			dropAuthHeaders(req.Header, extraAuthHeaders...)
		}
		return nil
	}
}

func dropAuthHeaders(header http.Header, extra ...string) {
	header.Del("Authorization")
	header.Del("PRIVATE-TOKEN")
	header.Del("JOB-TOKEN")
	header.Del("X-Api-Key")
	for _, name := range extra {
		if strings.TrimSpace(name) != "" {
			header.Del(name)
		}
	}
}
