package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	CodexClientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexUserCodeURL     = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	CodexPollURL         = "https://auth.openai.com/api/accounts/deviceauth/token"
	CodexVerificationURL = "https://auth.openai.com/codex/device"
	CodexRedirectURI     = "https://auth.openai.com/deviceauth/callback"
	CodexTokenURL        = "https://auth.openai.com/oauth/token"
	codexDefaultInterval = 5
	codexDefaultExpires  = 900
)

type codexUserCodeRequest struct {
	ClientID            string `json:"client_id"`
	CodeChallenge       string `json:"code_challenge,omitempty"`
	CodeChallengeMethod string `json:"code_challenge_method,omitempty"`
}

type codexUserCodeResponse struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	UserCodeAlt  string          `json:"usercode"`
	Interval     json.RawMessage `json:"interval"`
	ExpiresIn    int             `json:"expires_in"`
}

type codexPollRequest struct {
	DeviceAuthID string `json:"device_auth_id"`
	UserCode     string `json:"user_code"`
}

type codexPollResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
	CodeChallenge     string `json:"code_challenge"`
	Error             string `json:"error"`
}

func (o *DeviceOptions) codexDefaults() {
	o.commonDefaults()
	if o.ClientID == "" {
		o.ClientID = CodexClientID
	}
	if o.UserCodeURL == "" {
		o.UserCodeURL = CodexUserCodeURL
	}
	if o.PollURL == "" {
		o.PollURL = CodexPollURL
	}
	if o.VerificationURL == "" {
		o.VerificationURL = CodexVerificationURL
	}
	if o.RedirectURI == "" {
		o.RedirectURI = CodexRedirectURI
	}
	if o.TokenURL == "" {
		o.TokenURL = CodexTokenURL
	}
}

// LoginCodex runs the public Codex JSON device flow and stores tokens under "codex".
func LoginCodex(ctx context.Context, opts DeviceOptions) error {
	err := loginCodexDevice(ctx, opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	token, ok := readCodexCLIToken()
	if !ok {
		return err
	}
	fmt.Fprintln(os.Stderr, "native Codex login failed; importing ~/.codex/auth.json")
	if strings.TrimSpace(token.ClientID) == "" {
		token.ClientID = CodexClientID
	}
	if strings.TrimSpace(token.TokenURL) == "" {
		token.TokenURL = CodexTokenURL
	}
	return SaveToken("codex", token)
}

func loginCodexDevice(ctx context.Context, opts DeviceOptions) error {
	opts.codexDefaults()
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return err
	}
	device, err := requestCodexUserCode(ctx, opts, challenge)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "Open this URL in a browser and approve access:\n%s\n", opts.VerificationURL)
	if device.userCode != "" {
		fmt.Fprintf(opts.Stdout, "If asked, enter code: %s\n", device.userCode)
	}
	fmt.Fprintf(opts.Stdout, "Waiting for authorization…\n")
	code, pollVerifier, err := pollCodexAuthorization(ctx, opts, device)
	if err != nil {
		return err
	}
	if strings.TrimSpace(pollVerifier) != "" {
		verifier = pollVerifier
	}
	token, err := exchangeCodexCode(ctx, opts, code, verifier)
	if err != nil {
		return err
	}
	token.TokenURL = opts.TokenURL
	token.ClientID = opts.ClientID
	return SaveToken("codex", token)
}

type codexDevice struct {
	authID    string
	userCode  string
	interval  int
	expiresIn int
}

func requestCodexUserCode(ctx context.Context, opts DeviceOptions, challenge string) (codexDevice, error) {
	var decoded codexUserCodeResponse
	req := codexUserCodeRequest{ClientID: opts.ClientID}
	if challenge != "" {
		req.CodeChallenge = challenge
		req.CodeChallengeMethod = "S256"
	}
	if err := postJSON(ctx, opts.HTTP, opts.UserCodeURL, req, &decoded); err != nil {
		return codexDevice{}, fmt.Errorf("codex device usercode: %w", sanitizeDeviceError(err))
	}
	userCode := firstNonEmpty(strings.TrimSpace(decoded.UserCode), strings.TrimSpace(decoded.UserCodeAlt))
	if strings.TrimSpace(decoded.DeviceAuthID) == "" || userCode == "" {
		return codexDevice{}, errors.New("codex device usercode response is missing device_auth_id or user_code")
	}
	interval := parseIntervalSeconds(decoded.Interval, codexDefaultInterval)
	expires := decoded.ExpiresIn
	if expires <= 0 {
		expires = codexDefaultExpires
	}
	return codexDevice{
		authID:    strings.TrimSpace(decoded.DeviceAuthID),
		userCode:  userCode,
		interval:  interval,
		expiresIn: expires,
	}, nil
}

func pollCodexAuthorization(ctx context.Context, opts DeviceOptions, device codexDevice) (code, verifier string, err error) {
	deadline := opts.Now().Add(time.Duration(device.expiresIn) * time.Second)
	interval := time.Duration(device.interval) * time.Second
	req := codexPollRequest{DeviceAuthID: device.authID, UserCode: device.userCode}
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if !opts.Now().Before(deadline) {
			return "", "", errors.New("device authorization timed out")
		}
		var payload codexPollResponse
		err := postJSON(ctx, opts.HTTP, opts.PollURL, req, &payload)
		status := 0
		errCode := strings.TrimSpace(payload.Error)
		if err != nil {
			var apiErr *formError
			if errors.As(err, &apiErr) {
				status = apiErr.status
				errCode = firstNonEmpty(strings.TrimSpace(apiErr.payload.Error), errCode)
			} else {
				return "", "", err
			}
		}
		switch {
		case deniedDeviceError(errCode):
			return "", "", fmt.Errorf("device authorization failed: %s", errCode)
		case strings.TrimSpace(payload.AuthorizationCode) != "":
			return strings.TrimSpace(payload.AuthorizationCode), strings.TrimSpace(payload.CodeVerifier), nil
		case errCode == "slow_down":
			interval += 5 * time.Second
		case pendingDeviceError(status, errCode):
		default:
			if errCode != "" {
				return "", "", fmt.Errorf("device authorization failed: %s", errCode)
			}
			if err != nil {
				return "", "", sanitizeDeviceError(err)
			}
			return "", "", errors.New("codex device poll response is missing authorization_code")
		}
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		if !opts.Now().Before(deadline) {
			return "", "", errors.New("device authorization timed out")
		}
		opts.Sleep(interval)
	}
}

func exchangeCodexCode(ctx context.Context, opts DeviceOptions, code, verifier string) (Token, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"client_id":    {opts.ClientID},
		"code":         {code},
		"redirect_uri": {opts.RedirectURI},
	}
	if strings.TrimSpace(verifier) != "" {
		form.Set("code_verifier", verifier)
	}
	var payload tokenResponse
	if err := postForm(ctx, opts.HTTP, opts.TokenURL, form, &payload); err != nil {
		return Token{}, fmt.Errorf("codex token exchange: %w", sanitizeDeviceError(err))
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return Token{}, errors.New("token response is missing access_token")
	}
	return tokenFromResponse(payload, opts.Now()), nil
}

func pendingDeviceError(status int, code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authorization_pending", "deviceauth_authorization_pending":
		return true
	}
	if deniedDeviceError(code) {
		return false
	}
	return status == 403 || status == 404
}

func deniedDeviceError(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "access_denied", "deviceauth_access_denied", "expired_token":
		return true
	default:
		return false
	}
}

func sanitizeDeviceError(err error) error {
	var apiErr *formError
	if errors.As(err, &apiErr) {
		if apiErr.payload.Error != "" {
			return fmt.Errorf("HTTP %d: %s", apiErr.status, apiErr.payload.Error)
		}
		return fmt.Errorf("HTTP %d", apiErr.status)
	}
	return err
}

func parseIntervalSeconds(raw json.RawMessage, fallback int) int {
	if fallback <= 0 {
		fallback = codexDefaultInterval
	}
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	var asInt int
	if json.Unmarshal(raw, &asInt) == nil && asInt > 0 {
		return asInt
	}
	var asFloat float64
	if json.Unmarshal(raw, &asFloat) == nil && asFloat > 0 {
		return int(asFloat)
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(asString)); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
