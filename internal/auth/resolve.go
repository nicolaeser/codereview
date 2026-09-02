package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const refreshSkew = time.Hour

// Resolve returns a bearer token for provider. Env keys are resolved by config
// before this is called. Missing files are not an error.
func Resolve(ctx context.Context, provider string) (string, error) {
	provider = normalizeProvider(provider)
	if token, ok, err := storedToken(provider); err != nil {
		return "", err
	} else if ok {
		fresh, err := ensureFresh(ctx, provider, token)
		if err != nil {
			return "", err
		}
		return fresh.AccessToken, nil
	}
	if !UseLocalCredentials() {
		return "", nil
	}
	switch provider {
	case "grok":
		if token, ok := readGrokCLIToken(); ok {
			return token.AccessToken, nil
		}
	case "codex":
		if token, ok := readCodexCLIToken(); ok {
			return token.AccessToken, nil
		}
	}
	return "", nil
}

func ensureFresh(ctx context.Context, provider string, token Token) (Token, error) {
	if token.ExpiresAt.IsZero() || time.Until(token.ExpiresAt) > refreshSkew {
		return token, nil
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		return Token{}, fmt.Errorf("%s credentials expired; run `codereview login %s`", provider, provider)
	}
	fresh, err := refreshToken(ctx, provider, token)
	if err != nil {
		return Token{}, fmt.Errorf("refresh %s credentials: %w; run `codereview login %s`", provider, err, provider)
	}
	if err := SaveToken(provider, fresh); err != nil {
		return Token{}, err
	}
	return fresh, nil
}

func homeFile(parts ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

func readGrokCLIToken() (Token, bool) {
	path := homeFile(".grok", "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Token{}, false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return Token{}, false
	}
	if token, ok := tokenFromMap(rawObject(root)); ok {
		return token, true
	}
	var best Token
	var found bool
	for _, value := range root {
		var entry map[string]any
		if json.Unmarshal(value, &entry) != nil {
			continue
		}
		token, ok := tokenFromMap(entry)
		if !ok {
			continue
		}
		if !found || expiresLater(token, best) {
			best = token
			found = true
		}
	}
	return best, found
}

func readCodexCLIToken() (Token, bool) {
	path := homeFile(".codex", "auth.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Token{}, false
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Token{}, false
	}
	if token, ok := tokenFromMap(root); ok {
		return token, true
	}
	if nested, ok := root["tokens"].(map[string]any); ok {
		if token, ok := tokenFromMap(nested); ok {
			return token, true
		}
	}
	return Token{}, false
}

func rawObject(root map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for key, value := range root {
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			out[key] = decoded
		}
	}
	return out
}

func tokenFromMap(values map[string]any) (Token, bool) {
	access := stringField(values, "access_token", "key", "OPENAI_API_KEY")
	if access == "" {
		return Token{}, false
	}
	token := Token{
		AccessToken:  access,
		RefreshToken: stringField(values, "refresh_token"),
		TokenType:    firstNonEmpty(stringField(values, "token_type"), "Bearer"),
		TokenURL:     stringField(values, "token_url", "token_endpoint"),
		ClientID:     stringField(values, "client_id", "oidc_client_id"),
	}
	if exp := stringField(values, "expires_at"); exp != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, exp); err == nil {
			token.ExpiresAt = parsed
		} else if parsed, err := time.Parse(time.RFC3339, exp); err == nil {
			token.ExpiresAt = parsed
		}
	}
	return token, true
}

func stringField(values map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := values[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func expiresLater(left, right Token) bool {
	if left.ExpiresAt.IsZero() {
		return false
	}
	if right.ExpiresAt.IsZero() {
		return true
	}
	return left.ExpiresAt.After(right.ExpiresAt)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
