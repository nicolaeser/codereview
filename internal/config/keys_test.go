package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeysFileSyncAddsAndRemoves(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":"keep-me","anthropic":"drop-me"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_PROVIDER", "openai,openrouter-eu")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKey != "keep-me" {
		t.Fatalf("APIKey = %q, want keep-me", cfg.AI.APIKey)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["openai"] != "keep-me" {
		t.Fatalf("openai = %#v", parsed["openai"])
	}
	if _, ok := parsed["anthropic"]; ok {
		t.Fatalf("anthropic should have been removed: %s", raw)
	}
	if _, ok := parsed["openrouter-eu"]; !ok {
		t.Fatalf("openrouter-eu placeholder missing: %s", raw)
	}
}

func TestKeysFileOpenRouterEUOmitsPrivacyUnlessSet(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openrouter-eu":"or-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_PROVIDER", "openrouter-eu")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "provider/model")
	t.Setenv("AI_KEYS_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.BaseURL != "https://eu.openrouter.ai/api/v1" || !cfg.OpenRouter.EURouting {
		t.Fatalf("EU host = %#v %#v", cfg.AI, cfg.OpenRouter)
	}
	if cfg.OpenRouter.IncludeZDR || cfg.OpenRouter.IncludeDataCollection {
		t.Fatalf("privacy fields must be omitted when unset in keys file: %#v", cfg.OpenRouter)
	}

	if err := os.WriteFile(path, []byte(`{"openrouter-eu":{"key":"or-key","zdr":true,"data_collection":"deny"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OpenRouter.IncludeZDR || !cfg.OpenRouter.ZDR {
		t.Fatalf("zdr from keys file: %#v", cfg.OpenRouter)
	}
	if !cfg.OpenRouter.IncludeDataCollection || cfg.OpenRouter.DataCollection != "deny" {
		t.Fatalf("data_collection from keys file: %#v", cfg.OpenRouter)
	}
}

func TestKeysFileProviderModel(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	raw := `{
  "openai": {"key":"openai-key","model":"gpt-from-keys"},
  "anthropic": {"key":"ant-key","model":"claude-from-keys"}
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_MODEL", "")
	t.Setenv("AI_MODELS", "")
	t.Setenv("AI_PROVIDER", "openai,anthropic")
	t.Setenv("AI_KEYS_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "gpt-from-keys" {
		t.Fatalf("default models = %#v", cfg.AI.Models)
	}
	if err := cfg.SelectProvider("anthropic"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "claude-from-keys" {
		t.Fatalf("anthropic models = %#v", cfg.AI.Models)
	}
}

func TestKeysFileCustomOpenAIProvider(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	raw := `{
  "openai": "openai-key",
  "vllm": {"key":"local-key","base_url":"http://vllm:8000/v1"}
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_PROVIDER", "openai,vllm")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectProvider("vllm"); err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "vllm" || cfg.AI.BaseURL != "http://vllm:8000/v1" {
		t.Fatalf("custom provider = %#v", cfg.AI)
	}
	if cfg.AI.APIKey != "local-key" || cfg.AI.AuthMode != "bearer" {
		t.Fatalf("custom credentials = %#v", cfg.AI)
	}
}

func TestKeysJSONDoesNotWriteFile(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_KEYS_JSON", `{"openai":"from-env"}`)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "from-file") || strings.Contains(string(raw), "from-env") {
		t.Fatalf("AI_KEYS_JSON must not rewrite the file: %s", raw)
	}
}

func TestKeysFileRejectsBuiltinBaseURL(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":{"key":"k","base_url":"http://evil.example"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_FILE", path)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("Load() error = %v, want base_url rejection", err)
	}
}
