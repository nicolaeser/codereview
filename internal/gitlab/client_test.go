package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIsAutomationUser(t *testing.T) {
	if !IsAutomationUser(User{Username: "project_1_bot_hash", Bot: false}) {
		t.Fatal("project bot username should be automation")
	}
	if !IsAutomationUser(User{Username: "nico", Bot: true}) {
		t.Fatal("bot flag should be automation")
	}
	if IsAutomationUser(User{Username: "codereview", Bot: false}) {
		t.Fatal("human review user must not be treated as automation")
	}
}

func TestApproveAndUnapproveEndpoints(t *testing.T) {
	var approve, unapprove bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/unapprove"):
			unapprove = true
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"id":1}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/approve"):
			approve = true
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"id":1}`))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0)
	if err := client.ApproveMergeRequest(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := client.UnapproveMergeRequest(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	if !approve || !unapprove {
		t.Fatalf("approve=%t unapprove=%t", approve, unapprove)
	}
}

func TestListMergeRequestNotesStopsAfterMaxPagesAndSortsDesc(t *testing.T) {
	var pages int
	var firstQuery string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/notes") {
			http.NotFound(response, request)
			return
		}
		pages++
		if pages == 1 {
			firstQuery = request.URL.RawQuery
		}
		if got := request.URL.Query().Get("sort"); got != "desc" {
			t.Errorf("sort = %q, want desc", got)
		}
		if got := request.URL.Query().Get("order_by"); got != "created_at" {
			t.Errorf("order_by = %q, want created_at", got)
		}
		if got := request.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		batch := make([]map[string]any, 100)
		for i := range batch {
			batch[i] = map[string]any{"id": pages*100 + i, "body": "n"}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(batch)
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0)
	got, err := client.ListMergeRequestNotes(context.Background(), 7, 9)
	if err != nil {
		t.Fatal(err)
	}
	if pages != maxNotePages {
		t.Fatalf("pages = %d, want %d", pages, maxNotePages)
	}
	if len(got) != maxNotePages*100 {
		t.Fatalf("notes = %d, want %d", len(got), maxNotePages*100)
	}
	if !strings.Contains(firstQuery, "sort=desc") {
		t.Fatalf("query = %q, want sort=desc", firstQuery)
	}
}

func TestListDiscussionsStopsAfterMaxPages(t *testing.T) {
	var pages int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.Contains(request.URL.Path, "/discussions") {
			http.NotFound(response, request)
			return
		}
		pages++
		batch := make([]map[string]any, 100)
		for i := range batch {
			batch[i] = map[string]any{"id": strconv.Itoa(pages*100 + i), "notes": []map[string]any{}}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(batch)
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0)
	got, err := client.ListDiscussions(context.Background(), 7, 9)
	if err != nil {
		t.Fatal(err)
	}
	if pages != maxDiscussionPages {
		t.Fatalf("pages = %d, want %d", pages, maxDiscussionPages)
	}
	if len(got) != maxDiscussionPages*100 {
		t.Fatalf("discussions = %d, want %d", len(got), maxDiscussionPages*100)
	}
}

func TestAPIErrorStringOmitsBody(t *testing.T) {
	err := (&APIError{Method: "GET", URL: "https://gitlab.example.test/api/v4", Status: 500, Body: "sensitive response body"}).Error()
	if strings.Contains(err, "sensitive response body") {
		t.Fatalf("API error leaked body content: %q", err)
	}
}

func TestAPIErrorHintsOnForbidden(t *testing.T) {
	err := (&APIError{Method: "POST", URL: "https://gitlab.example.test/api/v4", Status: 403, Body: "denied"}).Error()
	if !strings.Contains(err, "token") || strings.Contains(err, "denied") {
		t.Fatalf("forbidden error = %q", err)
	}
}

func TestJobTokenHeaderUsedWhenConfigured(t *testing.T) {
	var sawJobToken, sawPrivateToken bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("JOB-TOKEN") == "ci-job" {
			sawJobToken = true
		}
		if request.Header.Get("PRIVATE-TOKEN") != "" {
			sawPrivateToken = true
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":1,"iid":2,"project_id":3,"state":"opened","title":"t","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"username":"dev"}}`))
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "ci-job", time.Second, true, 2)
	if _, err := client.GetMergeRequest(context.Background(), 3, 2); err != nil {
		t.Fatal(err)
	}
	if !sawJobToken || sawPrivateToken {
		t.Fatalf("jobToken=%t privateToken=%t", sawJobToken, sawPrivateToken)
	}
}

func TestPrivateTokenHeaderDefault(t *testing.T) {
	var sawPrivate string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sawPrivate = request.Header.Get("PRIVATE-TOKEN")
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":1,"iid":2,"project_id":3,"state":"opened","title":"t","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"username":"dev"}}`))
	}))
	t.Cleanup(server.Close)

	client := New(server.URL+"/api/v4", "pat-token", time.Second)
	if _, err := client.GetMergeRequest(context.Background(), 3, 2); err != nil {
		t.Fatal(err)
	}
	if sawPrivate != "pat-token" {
		t.Fatalf("PRIVATE-TOKEN = %q", sawPrivate)
	}
}

func TestGetRetriesTooManyRequestsThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodGet {
			t.Errorf("method = %s", request.Method)
		}
		if calls == 1 {
			response.Header().Set("Retry-After", "0")
			http.Error(response, `{"message":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":1,"iid":2,"project_id":3,"state":"opened","title":"t","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"username":"dev"}}`))
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 2)
	if _, err := client.GetMergeRequest(context.Background(), 3, 2); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestGetRetriesBadGatewayThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if calls == 1 {
			response.Header().Set("Retry-After", "0")
			http.Error(response, `{"message":"upstream"}`, http.StatusBadGateway)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":1,"iid":2,"project_id":3,"state":"opened","title":"t","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"username":"dev"}}`))
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 2)
	if _, err := client.GetMergeRequest(context.Background(), 3, 2); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestPostRetriesTooManyRequestsThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		if calls == 1 {
			response.Header().Set("Retry-After", "0")
			http.Error(response, `{"message":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":99,"body":"ok"}`))
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 2)
	if _, err := client.CreateNote(context.Background(), 3, 2, "body"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestPostIsNotRetriedOnBadGateway(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		http.Error(response, `{"message":"upstream"}`, http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 2)
	if _, err := client.CreateNote(context.Background(), 3, 2, "body"); err == nil {
		t.Fatal("expected 502 error")
	}
	if calls != 1 {
		t.Fatalf("POST retries on 502: calls = %d, want 1", calls)
	}
}

func TestCrossHostRedirectStripsGitLabTokens(t *testing.T) {
	t.Run("PRIVATE-TOKEN", func(t *testing.T) {
		destPrivate, destJob := runRedirectingGitLab(t, "secret-pat", false)
		if destPrivate != "" || destJob != "" {
			t.Fatalf("cross-host redirect leaked PRIVATE-TOKEN=%q JOB-TOKEN=%q", destPrivate, destJob)
		}
	})
	t.Run("JOB-TOKEN", func(t *testing.T) {
		destPrivate, destJob := runRedirectingGitLab(t, "ci-job", true)
		if destPrivate != "" || destJob != "" {
			t.Fatalf("cross-host redirect leaked PRIVATE-TOKEN=%q JOB-TOKEN=%q", destPrivate, destJob)
		}
	})
}

func runRedirectingGitLab(t *testing.T, token string, jobToken bool) (privateHeader, jobHeader string) {
	t.Helper()
	var destPrivate, destJob string
	dest := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		destPrivate = request.Header.Get("PRIVATE-TOKEN")
		destJob = request.Header.Get("JOB-TOKEN")
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":1,"iid":2,"project_id":3,"state":"opened","title":"t","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","author":{"username":"dev"}}`))
	}))
	t.Cleanup(dest.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, dest.URL+request.URL.Path, http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	client := NewWithOptions(origin.URL+"/api/v4", token, time.Second, jobToken, 0)
	if _, err := client.GetMergeRequest(context.Background(), 3, 2); err != nil {
		t.Fatal(err)
	}
	return destPrivate, destJob
}

func TestListMemberProjectIDsUsesMembershipFilter(t *testing.T) {
	var pages int
	var firstQuery string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/projects") {
			http.NotFound(response, request)
			return
		}
		if strings.Contains(request.URL.Path, "/projects/") {
			http.NotFound(response, request)
			return
		}
		pages++
		if pages == 1 {
			firstQuery = request.URL.RawQuery
		}
		if got := request.URL.Query().Get("membership"); got != "true" {
			t.Errorf("membership = %q, want true", got)
		}
		if got := request.URL.Query().Get("simple"); got != "true" {
			t.Errorf("simple = %q, want true", got)
		}
		batch := make([]map[string]any, memberProjectsPerPage)
		for i := range batch {
			batch[i] = map[string]any{"id": pages*1000 + i + 1}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(batch)
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0)
	got, err := client.ListMemberProjectIDs(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if pages != maxMemberProjectPages {
		t.Fatalf("pages = %d, want %d", pages, maxMemberProjectPages)
	}
	if len(got) != maxMemberProjectPages*memberProjectsPerPage {
		t.Fatalf("projects = %d, want %d", len(got), maxMemberProjectPages*memberProjectsPerPage)
	}
	if !strings.Contains(firstQuery, "membership=true") {
		t.Fatalf("query = %q, want membership=true", firstQuery)
	}
	if _, ok := got[1001]; !ok {
		t.Fatal("expected first page project id 1001")
	}
}

func TestListMemberProjectIDsStopsOnShortPageAndRespectsMax(t *testing.T) {
	var pages int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v4/projects" {
			http.NotFound(response, request)
			return
		}
		pages++
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode([]map[string]any{
			{"id": 12},
			{"id": 0},
			{"id": 34},
		})
	}))
	t.Cleanup(server.Close)

	client := NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0)
	got, err := client.ListMemberProjectIDs(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 {
		t.Fatalf("pages = %d, want 1", pages)
	}
	if len(got) != 2 {
		t.Fatalf("projects = %d, want 2", len(got))
	}
	if _, ok := got[12]; !ok {
		t.Fatal("missing project 12")
	}
	if _, ok := got[34]; !ok {
		t.Fatal("missing project 34")
	}
	if _, ok := got[0]; ok {
		t.Fatal("zero id must be omitted")
	}
}
