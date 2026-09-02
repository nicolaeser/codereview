package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const storeVersion = 1

// Token is a persisted OAuth or imported CLI credential. Never log these fields.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
}

type storeFile struct {
	Version   int              `json:"version"`
	Providers map[string]Token `json:"providers"`
}

func AuthPath() string {
	if path := strings.TrimSpace(os.Getenv("CODEREVIEW_AUTH_PATH")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".codereview", "auth.json")
}

func UseLocalCredentials() bool {
	if raw, ok := os.LookupEnv("CODEREVIEW_USE_LOCAL_CREDENTIALS"); ok && strings.TrimSpace(raw) != "" {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err == nil {
			return value
		}
	}
	return strings.TrimSpace(os.Getenv("CI")) == "" && strings.TrimSpace(os.Getenv("GITLAB_CI")) == ""
}

func loadStore(path string) (storeFile, error) {
	data := storeFile{Version: storeVersion, Providers: map[string]Token{}}
	if path == "" {
		return data, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return data, nil
		}
		return storeFile{}, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return storeFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if data.Providers == nil {
		data.Providers = map[string]Token{}
	}
	data.Version = storeVersion
	return data, nil
}

func saveStore(path string, data storeFile) error {
	if path == "" {
		return errors.New("auth store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data.Version = storeVersion
	if data.Providers == nil {
		data.Providers = map[string]Token{}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func SaveToken(provider string, token Token) error {
	provider = normalizeProvider(provider)
	if provider == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("access token is empty")
	}
	path := AuthPath()
	data, err := loadStore(path)
	if err != nil {
		return err
	}
	data.Providers[provider] = token
	for _, alias := range providerAliases(provider) {
		delete(data.Providers, alias)
	}
	return saveStore(path, data)
}

func DeleteToken(provider string) error {
	provider = normalizeProvider(provider)
	path := AuthPath()
	data, err := loadStore(path)
	if err != nil {
		return err
	}
	changed := false
	for _, key := range providerKeys(provider) {
		if _, ok := data.Providers[key]; ok {
			delete(data.Providers, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return saveStore(path, data)
}

func storedToken(provider string) (Token, bool, error) {
	data, err := loadStore(AuthPath())
	if err != nil {
		return Token{}, false, err
	}
	for _, key := range providerKeys(provider) {
		token, ok := data.Providers[key]
		if ok && strings.TrimSpace(token.AccessToken) != "" {
			return token, true, nil
		}
	}
	return Token{}, false, nil
}

func normalizeProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "xai", "grok", "grok-build", "x-ai", "spacexai":
		return "grok"
	case "openai", "codex", "openai-codex", "chatgpt":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func providerAliases(canonical string) []string {
	switch canonical {
	case "grok":
		return []string{"xai"}
	case "codex":
		return []string{"openai"}
	default:
		return nil
	}
}

func providerKeys(provider string) []string {
	canonical := normalizeProvider(provider)
	keys := []string{canonical}
	keys = append(keys, providerAliases(canonical)...)
	return keys
}
