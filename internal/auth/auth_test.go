package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginXAIDeviceFlow(t *testing.T) {
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/oauth2/device/code":
			writeJSON(response, map[string]any{
				"device_code": "dev-1", "user_code": "ABCD-EFGH",
				"verification_uri":          "https://accounts.x.ai/oauth2/device",
				"verification_uri_complete": "https://accounts.x.ai/oauth2/device?user_code=ABCD-EFGH",
				"expires_in":                600, "interval": 1,
			})
		case request.URL.Path == "/oauth2/token":
			n := polls.Add(1)
			if n == 1 {
				response.WriteHeader(http.StatusBadRequest)
				writeJSON(response, map[string]any{"error": "authorization_pending"})
				return
			}
			writeJSON(response, map[string]any{
				"access_token": "access-xyz", "refresh_token": "refresh-xyz",
				"token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	var stdout strings.Builder
	err := LoginXAI(context.Background(), DeviceOptions{
		DeviceURL: server.URL + "/oauth2/device/code",
		TokenURL:  server.URL + "/oauth2/token",
		HTTP:      server.Client(),
		Stdout:    &stdout,
		Sleep:     func(time.Duration) {},
		Now:       func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ABCD-EFGH") {
		t.Fatalf("stdout missing user code: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "access-xyz") {
		t.Fatal("access token leaked to stdout")
	}
	token, ok, err := storedToken("xai")
	if err != nil || !ok || token.AccessToken != "access-xyz" || token.RefreshToken != "refresh-xyz" {
		t.Fatalf("stored token = %#v ok=%t err=%v", token, ok, err)
	}
	info, err := os.Stat(AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file mode = %v", info.Mode().Perm())
	}
}

func TestLoginXAIAccessDenied(t *testing.T) {
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/device/code") {
			writeJSON(response, map[string]any{
				"device_code": "dev-1", "user_code": "ABCD",
				"verification_uri": "https://example.test/device",
				"expires_in":       60, "interval": 1,
			})
			return
		}
		response.WriteHeader(http.StatusBadRequest)
		writeJSON(response, map[string]any{"error": "access_denied"})
	}))
	t.Cleanup(server.Close)
	err := LoginXAI(context.Background(), DeviceOptions{
		DeviceURL: server.URL + "/oauth2/device/code",
		TokenURL:  server.URL + "/oauth2/token",
		HTTP:      server.Client(),
		Stdout:    io.Discard,
		Sleep:     func(time.Duration) {},
		Now:       time.Now,
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "dev-1") {
		t.Fatalf("device code leaked: %v", err)
	}
}

func TestResolveSkipsHomeFilesInCI(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CODEREVIEW_USE_LOCAL_CREDENTIALS", "")
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "missing.json"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".grok", "auth.json"), []byte(`{"https://auth.x.ai::id":{"key":"grok-secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := Resolve(context.Background(), "xai")
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		t.Fatalf("CI resolved home token %q", token)
	}
}

func TestResolveReadsGrokCLIWhenLocal(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("CODEREVIEW_USE_LOCAL_CREDENTIALS", "true")
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "missing.json"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": map[string]any{
			"key": "grok-local-token", "refresh_token": "ref",
		},
	}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(home, ".grok", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := Resolve(context.Background(), "grok")
	if err != nil {
		t.Fatal(err)
	}
	if token != "grok-local-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoginCodexDeviceFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	const (
		userCode     = "WXYZ-1234"
		accessToken  = "access-codex"
		refreshToken = "refresh-codex"
		authCode     = "auth-code-1"
		pollVerifier = "pkce-verifier-from-poll"
	)
	var polls atomic.Int32
	var gotVerifier atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/deviceauth/usercode"):
			writeJSON(response, map[string]any{
				"device_auth_id": "dev-codex",
				"user_code":      userCode,
				"interval":       "1",
				"expires_in":     600,
			})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/deviceauth/token"):
			n := polls.Add(1)
			if n == 1 {
				response.WriteHeader(http.StatusForbidden)
				writeJSON(response, map[string]any{"error": "deviceauth_authorization_pending"})
				return
			}
			writeJSON(response, map[string]any{
				"authorization_code": authCode,
				"code_verifier":      pollVerifier,
				"code_challenge":     "challenge",
			})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/oauth/token"):
			if err := request.ParseForm(); err != nil {
				http.Error(response, "form", http.StatusBadRequest)
				return
			}
			if request.PostFormValue("grant_type") != "authorization_code" || request.PostFormValue("code") != authCode {
				http.Error(response, "bad exchange", http.StatusBadRequest)
				return
			}
			if request.PostFormValue("code_verifier") == pollVerifier {
				gotVerifier.Store(true)
			}
			writeJSON(response, map[string]any{
				"access_token": accessToken, "refresh_token": refreshToken,
				"token_type": "Bearer", "expires_in": 3600,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	var stdout strings.Builder
	err := LoginCodex(context.Background(), DeviceOptions{
		UserCodeURL:     server.URL + "/api/accounts/deviceauth/usercode",
		PollURL:         server.URL + "/api/accounts/deviceauth/token",
		TokenURL:        server.URL + "/oauth/token",
		VerificationURL: "https://auth.openai.com/codex/device",
		RedirectURI:     server.URL + "/deviceauth/callback",
		HTTP:            server.Client(),
		Stdout:          &stdout,
		Sleep:           func(time.Duration) {},
		Now:             func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, userCode) || !strings.Contains(out, "https://auth.openai.com/codex/device") {
		t.Fatalf("stdout missing user code or URL: %s", out)
	}
	assertNoSecrets(t, out, accessToken, refreshToken, authCode, pollVerifier, "dev-codex")
	if !gotVerifier.Load() {
		t.Fatal("token exchange did not send poll code_verifier")
	}
	token, ok, err := storedToken("codex")
	if err != nil || !ok || token.AccessToken != accessToken || token.RefreshToken != refreshToken {
		t.Fatalf("stored token = %#v ok=%t err=%v", token, ok, err)
	}
	if _, ok, err := storedToken("openai"); err != nil || !ok {
		t.Fatalf("openai alias missing stored codex token ok=%t err=%v", ok, err)
	}
	info, err := os.Stat(AuthPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth file mode = %v", info.Mode().Perm())
	}
}

func TestLoginCodexAccessDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/deviceauth/usercode") {
			writeJSON(response, map[string]any{
				"device_auth_id": "dev-codex", "user_code": "ABCD",
				"interval": 1, "expires_in": 60,
			})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/deviceauth/token") {
			response.WriteHeader(http.StatusBadRequest)
			writeJSON(response, map[string]any{
				"error":             "access_denied",
				"error_description": "token access-secret device_auth_id=dev-codex",
			})
			return
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(server.Close)
	err := LoginCodex(context.Background(), DeviceOptions{
		UserCodeURL:     server.URL + "/api/accounts/deviceauth/usercode",
		PollURL:         server.URL + "/api/accounts/deviceauth/token",
		TokenURL:        server.URL + "/oauth/token",
		VerificationURL: "https://auth.openai.com/codex/device",
		HTTP:            server.Client(),
		Stdout:          io.Discard,
		Sleep:           func(time.Duration) {},
		Now:             time.Now,
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error = %v", err)
	}
	assertNoSecrets(t, err.Error(), "dev-codex", "access-secret")
}

func TestLoginCodexTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var elapsed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/deviceauth/usercode") {
			writeJSON(response, map[string]any{
				"device_auth_id": "dev-codex", "usercode": "ABCD",
				"interval": 1, "expires_in": 2,
			})
			return
		}
		response.WriteHeader(http.StatusForbidden)
		writeJSON(response, map[string]any{"error": "authorization_pending"})
	}))
	t.Cleanup(server.Close)
	err := LoginCodex(context.Background(), DeviceOptions{
		UserCodeURL: server.URL + "/api/accounts/deviceauth/usercode",
		PollURL:     server.URL + "/api/accounts/deviceauth/token",
		TokenURL:    server.URL + "/oauth/token",
		HTTP:        server.Client(),
		Stdout:      io.Discard,
		Sleep: func(d time.Duration) {
			elapsed.Add(int64(d))
		},
		Now: func() time.Time { return start.Add(time.Duration(elapsed.Load())) },
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v", err)
	}
	assertNoSecrets(t, err.Error(), "dev-codex", "ABCD")
}

func TestResolveReadsCodexCLIWhenLocal(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("GITLAB_CI", "")
	t.Setenv("CODEREVIEW_USE_LOCAL_CREDENTIALS", "true")
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "missing.json"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"tokens": map[string]any{"access_token": "codex-local-token", "refresh_token": "ref"},
	}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := Resolve(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if token != "codex-local-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestResolveLegacyStoreKeys(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CODEREVIEW_USE_LOCAL_CREDENTIALS", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("CODEREVIEW_AUTH_PATH", path)
	raw := []byte(`{"version":1,"providers":{"xai":{"access_token":"legacy-xai"},"openai":{"access_token":"legacy-openai"}}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	grok, err := Resolve(context.Background(), "grok")
	if err != nil {
		t.Fatal(err)
	}
	if grok != "legacy-xai" {
		t.Fatalf("grok = %q", grok)
	}
	xai, err := Resolve(context.Background(), "xai")
	if err != nil {
		t.Fatal(err)
	}
	if xai != "legacy-xai" {
		t.Fatalf("xai = %q", xai)
	}
	codex, err := Resolve(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if codex != "legacy-openai" {
		t.Fatalf("codex = %q", codex)
	}
}

func writeJSON(response http.ResponseWriter, payload any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}

func assertNoSecrets(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("secret leaked")
		}
	}
}
