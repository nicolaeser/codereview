package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	XAIClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	XAIScope        = "openid profile email offline_access grok-cli:access api:access"
	XAIDiscoveryURL = "https://auth.x.ai/.well-known/openid-configuration"
	XAIDeviceURL    = "https://auth.x.ai/oauth2/device/code"
	XAITokenURL     = "https://auth.x.ai/oauth2/token"
)

// DeviceOptions configures device login. Tests inject endpoints and a fake clock.
// Grok uses RFC 8628 DeviceURL + TokenURL. Codex uses JSON UserCodeURL + PollURL + TokenURL.
type DeviceOptions struct {
	ClientID        string
	Scope           string
	DiscoveryURL    string
	DeviceURL       string
	TokenURL        string
	UserCodeURL     string
	PollURL         string
	VerificationURL string
	RedirectURI     string
	HTTP            *http.Client
	Stdout          io.Writer
	Sleep           func(time.Duration)
	Now             func() time.Time
}

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func (o *DeviceOptions) commonDefaults() {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

func (o *DeviceOptions) defaults() {
	o.commonDefaults()
	if o.ClientID == "" {
		o.ClientID = XAIClientID
	}
	if o.Scope == "" {
		o.Scope = XAIScope
	}
	if o.DiscoveryURL == "" {
		o.DiscoveryURL = XAIDiscoveryURL
	}
}

// LoginXAI runs the xAI device-code flow and stores tokens in the CodeReview auth file.
func LoginXAI(ctx context.Context, opts DeviceOptions) error {
	opts.defaults()
	if opts.DeviceURL == "" || opts.TokenURL == "" {
		deviceURL, tokenURL, err := discoverEndpoints(ctx, opts.HTTP, opts.DiscoveryURL)
		if err != nil {
			if opts.DeviceURL == "" {
				opts.DeviceURL = XAIDeviceURL
			}
			if opts.TokenURL == "" {
				opts.TokenURL = XAITokenURL
			}
		} else {
			if opts.DeviceURL == "" {
				opts.DeviceURL = deviceURL
			}
			if opts.TokenURL == "" {
				opts.TokenURL = tokenURL
			}
		}
	}
	device, err := requestDeviceCode(ctx, opts)
	if err != nil {
		return err
	}
	verify := device.VerificationURIComplete
	if verify == "" {
		verify = device.VerificationURI
	}
	fmt.Fprintf(opts.Stdout, "Open this URL in a browser and approve access:\n%s\n", verify)
	if device.UserCode != "" {
		fmt.Fprintf(opts.Stdout, "If asked, enter code: %s\n", device.UserCode)
	}
	fmt.Fprintf(opts.Stdout, "Waiting for authorization…\n")
	token, err := pollDeviceToken(ctx, opts, device)
	if err != nil {
		return err
	}
	token.TokenURL = opts.TokenURL
	token.ClientID = opts.ClientID
	return SaveToken("grok", token)
}

func discoverEndpoints(ctx context.Context, client *http.Client, discoveryURL string) (deviceURL, tokenURL string, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("OIDC discovery returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
		TokenEndpoint               string `json:"token_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", "", err
	}
	if payload.DeviceAuthorizationEndpoint == "" || payload.TokenEndpoint == "" {
		return "", "", errors.New("OIDC discovery is missing device or token endpoints")
	}
	return payload.DeviceAuthorizationEndpoint, payload.TokenEndpoint, nil
}

func requestDeviceCode(ctx context.Context, opts DeviceOptions) (deviceCodeResponse, error) {
	form := url.Values{
		"client_id": {opts.ClientID},
		"scope":     {opts.Scope},
	}
	var decoded deviceCodeResponse
	if err := postForm(ctx, opts.HTTP, opts.DeviceURL, form, &decoded); err != nil {
		return deviceCodeResponse{}, err
	}
	if decoded.DeviceCode == "" || decoded.VerificationURI == "" && decoded.VerificationURIComplete == "" {
		return deviceCodeResponse{}, errors.New("device authorization response is missing device_code or verification_uri")
	}
	if decoded.Interval <= 0 {
		decoded.Interval = 5
	}
	if decoded.ExpiresIn <= 0 {
		decoded.ExpiresIn = 900
	}
	return decoded, nil
}

func pollDeviceToken(ctx context.Context, opts DeviceOptions, device deviceCodeResponse) (Token, error) {
	deadline := opts.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	interval := time.Duration(device.Interval) * time.Second
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"client_id":   {opts.ClientID},
		"device_code": {device.DeviceCode},
	}
	for {
		if err := ctx.Err(); err != nil {
			return Token{}, err
		}
		if opts.Now().After(deadline) {
			return Token{}, errors.New("device authorization timed out")
		}
		opts.Sleep(interval)
		var payload tokenResponse
		err := postForm(ctx, opts.HTTP, opts.TokenURL, form, &payload)
		if err != nil {
			var apiErr *formError
			if errors.As(err, &apiErr) && apiErr.payload.Error != "" {
				payload = apiErr.payload
			} else {
				return Token{}, err
			}
		}
		switch payload.Error {
		case "", "success":
			if strings.TrimSpace(payload.AccessToken) == "" {
				if payload.Error != "" {
					break
				}
				return Token{}, errors.New("token response is missing access_token")
			}
			return tokenFromResponse(payload, opts.Now()), nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token", "access_denied":
			return Token{}, fmt.Errorf("device authorization failed: %s", payload.Error)
		default:
			if payload.Error != "" {
				return Token{}, fmt.Errorf("device authorization failed: %s", payload.Error)
			}
		}
	}
}

func refreshToken(ctx context.Context, provider string, token Token) (Token, error) {
	tokenURL := strings.TrimSpace(token.TokenURL)
	clientID := strings.TrimSpace(token.ClientID)
	if tokenURL == "" || clientID == "" {
		switch normalizeProvider(provider) {
		case "codex":
			if tokenURL == "" {
				tokenURL = CodexTokenURL
			}
			if clientID == "" {
				clientID = CodexClientID
			}
		default:
			if tokenURL == "" {
				tokenURL = XAITokenURL
			}
			if clientID == "" {
				clientID = XAIClientID
			}
		}
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {token.RefreshToken},
	}
	client := &http.Client{Timeout: 30 * time.Second}
	var payload tokenResponse
	if err := postForm(ctx, client, tokenURL, form, &payload); err != nil {
		return Token{}, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return Token{}, errors.New("refresh response is missing access_token")
	}
	fresh := tokenFromResponse(payload, time.Now())
	fresh.TokenURL = tokenURL
	fresh.ClientID = clientID
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = token.RefreshToken
	}
	return fresh, nil
}

func tokenFromResponse(payload tokenResponse, now time.Time) Token {
	expires := 6 * time.Hour
	if payload.ExpiresIn > 0 {
		expires = time.Duration(payload.ExpiresIn) * time.Second
	}
	return Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    firstNonEmpty(payload.TokenType, "Bearer"),
		ExpiresAt:    now.Add(expires),
	}
}

type formError struct {
	status  int
	payload tokenResponse
}

func (e *formError) Error() string {
	if e.payload.Error != "" {
		return fmt.Sprintf("HTTP %d: %s", e.status, e.payload.Error)
	}
	return fmt.Sprintf("HTTP %d", e.status)
}

func postForm(ctx context.Context, client *http.Client, target string, form url.Values, dest any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &formError{status: response.StatusCode, payload: decodeOAuthError(body)}
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	return nil
}

func postJSON(ctx context.Context, client *http.Client, target string, payload any, dest any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &formError{status: response.StatusCode, payload: decodeOAuthError(body)}
	}
	if dest == nil {
		return nil
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode device response: %w", err)
	}
	return nil
}

func decodeOAuthError(body []byte) tokenResponse {
	var payload tokenResponse
	_ = json.Unmarshal(body, &payload)
	if strings.TrimSpace(payload.Error) != "" {
		payload.Error = strings.TrimSpace(payload.Error)
		payload.ErrorDesc = ""
		return payload
	}
	var nested struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &nested) != nil || len(nested.Error) == 0 {
		payload.ErrorDesc = ""
		return payload
	}
	if nested.Error[0] == '"' {
		var code string
		if json.Unmarshal(nested.Error, &code) == nil {
			payload.Error = strings.TrimSpace(code)
		}
		payload.ErrorDesc = ""
		return payload
	}
	var obj struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(nested.Error, &obj) == nil {
		payload.Error = firstNonEmpty(strings.TrimSpace(obj.Code), strings.TrimSpace(obj.Type))
	}
	payload.ErrorDesc = ""
	return payload
}
