package config

import (
	"strings"
	"testing"
)

func TestParseRepoFileMissingOrEmptyIsNoop(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("   \n"), []byte("# only comments\n")} {
		file, err := ParseRepoFile(raw)
		if err != nil {
			t.Fatalf("ParseRepoFile(%q) error = %v", raw, err)
		}
		if file.Review.Mode != nil || file.Review.Drafts != nil || file.Review.DependencyBumps != nil || file.Review.ApproveDependencyBumps != nil || file.Review.IgnorePaths != nil {
			t.Fatalf("empty policy parsed as %#v", file.Review)
		}
	}
}

func TestParseRepoFileValidAndForbiddenKeysIgnored(t *testing.T) {
	file, err := ParseRepoFile([]byte(`
ai:
  api_key: secret
  provider: openai
  base_url: https://evil.example.test
gitlab_token: secret
instruction_path: /etc/passwd
job_timeout: 1h
state_path: /tmp/state.json
review:
  mode: deep
  drafts: true
  dependency_bumps: true
  approve_dependency_bumps: true
  dependency_bump_mode: validate
  dependency_bump_push_url: https://evil.example.test/hook
  dependency_bump_push_secret: stolen
  ignore_paths: ["vendor/**", "tmp/**"]
  mention: codereview
  mention_aliases: ["cr"]
  path_instructions:
    - path: "internal/ai/**"
      instructions: "Prefer protocol-level findings."
  ai_api_key: also-secret
  gitlab_url: https://evil.example.test
  generate_version_bumps: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if file.Review.Mode == nil || *file.Review.Mode != "deep" {
		t.Fatalf("mode = %v", file.Review.Mode)
	}
	if file.Review.Drafts == nil || !*file.Review.Drafts {
		t.Fatal("drafts not parsed")
	}
	if file.Review.DependencyBumps == nil || !*file.Review.DependencyBumps {
		t.Fatal("dependency_bumps not parsed")
	}
	if file.Review.ApproveDependencyBumps == nil || !*file.Review.ApproveDependencyBumps {
		t.Fatal("approve_dependency_bumps not parsed")
	}
	if file.Review.DependencyBumpMode == nil || *file.Review.DependencyBumpMode != "validate" {
		t.Fatal("dependency_bump_mode not parsed")
	}
	if file.Review.BumpPushURL == nil || *file.Review.BumpPushURL != "https://evil.example.test/hook" {
		t.Fatal("yaml PUSH URL must parse so ApplyRepoFile can ignore it")
	}
	if file.Review.BumpPushSecret == nil || *file.Review.BumpPushSecret != "stolen" {
		t.Fatal("yaml PUSH secret must parse so ApplyRepoFile can ignore it")
	}
	if len(file.Review.IgnorePaths) != 2 || file.Review.IgnorePaths[0] != "vendor/**" {
		t.Fatalf("ignore_paths = %#v", file.Review.IgnorePaths)
	}
	if len(file.Review.PathInstructions) != 1 || file.Review.PathInstructions[0].Path != "internal/ai/**" {
		t.Fatalf("path_instructions = %#v", file.Review.PathInstructions)
	}
}

func TestParseRepoFileMalformedFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "invalid yaml", raw: "review: ["},
		{name: "drafts wrong type", raw: "review:\n  drafts: [true]\n"},
		{name: "dependency_bumps wrong type", raw: "review:\n  dependency_bumps: [true]\n"},
		{name: "approve_dependency_bumps wrong type", raw: "review:\n  approve_dependency_bumps: [true]\n"},
		{name: "dependency_bump_mode wrong type", raw: "review:\n  dependency_bump_mode: [notify]\n"},
		{name: "ignore_paths wrong type", raw: "review:\n  ignore_paths: vendor/**\n"},
		{name: "path_instructions wrong type", raw: "review:\n  path_instructions: true\n"},
		{name: "skip_ack wrong type", raw: "review:\n  skip_ack: [true]\n"},
		{name: "skip_tokens wrong type", raw: "review:\n  skip_tokens: true\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseRepoFile([]byte(test.raw)); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
}

func TestParseRepoFileRejectsOversizedInput(t *testing.T) {
	raw := []byte("review:\n  drafts: true\n" + strings.Repeat("#", maxRepoFileBytes))
	if _, err := ParseRepoFile(raw); err == nil {
		t.Fatal("expected oversized policy to fail")
	}
}

func TestApplyRepoFileSetsIgnorePathsAndDraftsWhenEnvUnset(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  drafts: true
  ignore_paths: ["tmp/**"]
  path_instructions:
    - path: "internal/ai/**"
      instructions: "Check provider routing."
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if !cfg.Review.ReviewDrafts {
		t.Fatal("yaml drafts=true should apply when REVIEW_DRAFTS is unset")
	}
	if len(cfg.Review.IgnorePaths) == 0 || cfg.Review.IgnorePaths[0] != "tmp/**" {
		t.Fatalf("yaml ignore_paths should prepend operator/default paths, got %#v", cfg.Review.IgnorePaths)
	}
	if !containsString(cfg.Review.IgnorePaths, "vendor/**") {
		t.Fatalf("compiled IGNORE_PATHS defaults should remain after merge: %#v", cfg.Review.IgnorePaths)
	}
	if len(cfg.Review.PathInstructions) != 1 || cfg.Review.PathInstructions[0].Path != "internal/ai/**" {
		t.Fatalf("path_instructions = %#v", cfg.Review.PathInstructions)
	}
}

func TestApplyRepoFileSetsDependencyBumpsWhenEnvUnset(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  dependency_bumps: true
  approve_dependency_bumps: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if !cfg.Review.DependencyBumps {
		t.Fatal("yaml dependency_bumps=true should apply when REVIEW_DEPENDENCY_BUMPS is unset")
	}
	if !cfg.Review.ApproveDependencyBumps {
		t.Fatal("yaml approve_dependency_bumps=true should apply when GITLAB_APPROVE_DEPENDENCY_BUMPS is unset")
	}
}

func TestApplyRepoFileSetsDependencyBumpModeWhenEnvUnset(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  dependency_bump_mode: notify\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.DependencyBumpMode != DependencyBumpModeNotify {
		t.Fatalf("yaml mode = %q, want notify", cfg.Review.DependencyBumpMode)
	}
}

func TestApplyRepoFileEnvDependencyBumpModeWinsOverYAML(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "wait")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  dependency_bump_mode: notify\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.DependencyBumpMode != DependencyBumpModeWait {
		t.Fatalf("env mode = %q, want wait", cfg.Review.DependencyBumpMode)
	}
}

func TestApplyRepoFileIgnoresYamlPushURLAndSecret(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "validate")
	t.Setenv("DEPENDENCY_BUMP_PUSH_URL", "https://agent.example.test/hook")
	t.Setenv("DEPENDENCY_BUMP_PUSH_SECRET", "operator-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  dependency_bump_mode: notify
  dependency_bump_push_url: https://evil.example.test/hook
  dependency_bump_push_secret: stolen
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.DependencyBumpMode != DependencyBumpModeValidate {
		t.Fatalf("operator mode must win, got %q", cfg.Review.DependencyBumpMode)
	}
	if cfg.Review.BumpPushURL != "https://agent.example.test/hook" {
		t.Fatalf("yaml PUSH URL leaked: %q", cfg.Review.BumpPushURL)
	}
	if cfg.Review.BumpPushSecret != "operator-secret" {
		t.Fatalf("yaml PUSH secret leaked: %q", cfg.Review.BumpPushSecret)
	}
}

func TestApplyRepoFileYamlPushModeFailsClosedAndDoesNotCopyURL(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  dependency_bump_mode: push
  dependency_bump_push_url: https://evil.example.test/hook
  dependency_bump_push_secret: stolen
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err == nil {
		t.Fatal("expected yaml push mode to fail closed")
	}
	if cfg.Review.BumpPushURL != "" || cfg.Review.BumpPushSecret != "" {
		t.Fatalf("yaml PUSH fields must not be copied: url=%q secret=%q", cfg.Review.BumpPushURL, cfg.Review.BumpPushSecret)
	}
}

func TestApplyRepoFileInvalidDependencyBumpModeFailsClosed(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  dependency_bump_mode: automerge\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err == nil {
		t.Fatal("expected invalid bump mode to fail")
	}
}

func TestApplyRepoFileEnvDependencyBumpsWinsOverYAML(t *testing.T) {
	t.Run("REVIEW_DEPENDENCY_BUMPS", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMPS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		file, err := ParseRepoFile([]byte("review:\n  dependency_bumps: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ApplyRepoFile(file); err != nil {
			t.Fatal(err)
		}
		if !cfg.Review.DependencyBumps {
			t.Fatal("REVIEW_DEPENDENCY_BUMPS=true must win over yaml dependency_bumps: false")
		}
	})
	t.Run("GITLAB_APPROVE_DEPENDENCY_BUMPS", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		file, err := ParseRepoFile([]byte("review:\n  approve_dependency_bumps: false\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ApplyRepoFile(file); err != nil {
			t.Fatal(err)
		}
		if !cfg.Review.ApproveDependencyBumps {
			t.Fatal("GITLAB_APPROVE_DEPENDENCY_BUMPS=true must win over yaml approve_dependency_bumps: false")
		}
	})
}

func TestApplyRepoFileSkipTokensAndAck(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  skip_tokens: ["wip"]
  skip_ack: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if !containsString(cfg.Review.SkipTokens, "wip") || !containsString(cfg.Review.SkipTokens, "skip-codereview") {
		t.Fatalf("yaml skip_tokens should merge with defaults, got %#v", cfg.Review.SkipTokens)
	}
	if !cfg.Review.SkipAck {
		t.Fatal("yaml skip_ack=true should apply when SKIP_MR_ACK is unset")
	}
}

func TestApplyRepoFileEnvSkipAckWinsOverYAML(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("SKIP_MR_ACK", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  skip_ack: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.SkipAck {
		t.Fatal("SKIP_MR_ACK=false must win over yaml skip_ack: true")
	}
}

func TestApplyRepoFileEnvDraftsWinsOverYAML(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("REVIEW_DRAFTS", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  drafts: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if !cfg.Review.ReviewDrafts {
		t.Fatal("REVIEW_DRAFTS=true must win over yaml drafts: false")
	}
}

func TestApplyRepoFileEnvModeAndCLIModeWin(t *testing.T) {
	t.Run("REVIEW_MODE", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_MODE", "quick")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		file, err := ParseRepoFile([]byte("review:\n  mode: deep\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ApplyRepoFile(file); err != nil {
			t.Fatal(err)
		}
		if cfg.Target.Mode != "quick" {
			t.Fatalf("Target.Mode = %q, want quick", cfg.Target.Mode)
		}
	})
	t.Run("DEFAULT_REVIEW_MODE", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("DEFAULT_REVIEW_MODE", "quick")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		file, err := ParseRepoFile([]byte("review:\n  mode: security\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ApplyRepoFile(file); err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DefaultMode != "quick" {
			t.Fatalf("DefaultMode = %q, want quick", cfg.Review.DefaultMode)
		}
	})
	t.Run("cli --mode already on Target", func(t *testing.T) {
		setBaseConfigEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		cfg.ApplyTargetOverrides(0, 0, "quick", false, false, false, false, false, false, false, false)
		file, err := ParseRepoFile([]byte("review:\n  mode: deep\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.ApplyRepoFile(file); err != nil {
			t.Fatal(err)
		}
		if cfg.Target.Mode != "quick" {
			t.Fatalf("CLI Target.Mode = %q, want quick", cfg.Target.Mode)
		}
		if cfg.Review.DefaultMode != "deep" {
			t.Fatalf("yaml may still fill unset DEFAULT_REVIEW_MODE, got %q", cfg.Review.DefaultMode)
		}
	})
}

func TestApplyRepoFileMergesIgnorePathsOperatorLast(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("IGNORE_PATHS", "secret/**")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  ignore_paths: [\"tmp/**\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Review.IgnorePaths) != 2 || cfg.Review.IgnorePaths[0] != "tmp/**" || cfg.Review.IgnorePaths[1] != "secret/**" {
		t.Fatalf("merged ignore_paths = %#v", cfg.Review.IgnorePaths)
	}
}

func TestApplyRepoFileMergesPathInstructionsOperatorLast(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("PATH_INSTRUCTIONS_JSON", `[{"path":"cmd/**","instructions":"operator"}]`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte(`
review:
  path_instructions:
    - path: "internal/ai/**"
      instructions: "repository"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Review.PathInstructions) != 2 {
		t.Fatalf("merged instructions = %#v", cfg.Review.PathInstructions)
	}
	if cfg.Review.PathInstructions[0].Path != "internal/ai/**" || cfg.Review.PathInstructions[1].Path != "cmd/**" {
		t.Fatalf("merge order = %#v", cfg.Review.PathInstructions)
	}
}

func TestApplyRepoFileYAMLModeWinsCompiledDefault(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.DefaultMode != "standard" {
		t.Fatalf("precondition DefaultMode = %q", cfg.Review.DefaultMode)
	}
	file, err := ParseRepoFile([]byte("review:\n  mode: security\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.Review.DefaultMode != "security" {
		t.Fatalf("DefaultMode = %q, want security", cfg.Review.DefaultMode)
	}
}

func TestApplyRepoFileInvalidModeFailsClosed(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	file, err := ParseRepoFile([]byte("review:\n  mode: turbo\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err == nil {
		t.Fatal("expected invalid mode to fail")
	}
}

func TestApplyRepoFileDoesNotClobberSecretsOrRouting(t *testing.T) {
	setBaseConfigEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	token, apiURL, key, provider, base := cfg.GitLab.Token, cfg.GitLab.APIURL, cfg.AI.APIKey, cfg.AI.Provider, cfg.AI.BaseURL
	file, err := ParseRepoFile([]byte(`
gitlab_token: stolen
ai:
  api_key: stolen
  provider: anthropic
  base_url: https://evil.example.test
review:
  drafts: true
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ApplyRepoFile(file); err != nil {
		t.Fatal(err)
	}
	if cfg.GitLab.Token != token || cfg.GitLab.APIURL != apiURL || cfg.AI.APIKey != key || cfg.AI.Provider != provider || cfg.AI.BaseURL != base {
		t.Fatalf("forbidden keys leaked into config: %#v %#v", cfg.GitLab, cfg.AI)
	}
	if !cfg.Review.ReviewDrafts {
		t.Fatal("allowed drafts key should still apply")
	}
}

func TestApplyRepoFileProviderAndModel(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		yaml    string
		wantErr string
		check   func(*testing.T, Config)
	}{
		{
			name: "yaml provider+model apply when allowlisted",
			env: map[string]string{
				"AI_PROVIDER":  "openai,anthropic",
				"AI_KEYS_JSON": `{"anthropic":"ant-key"}`,
			},
			yaml: `
review:
  provider: anthropic
  model: claude-sonnet
`,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "anthropic" {
					t.Fatalf("provider = %q, want anthropic", cfg.AI.Provider)
				}
				if cfg.AI.BaseURL != "https://api.anthropic.com" {
					t.Fatalf("BaseURL = %q", cfg.AI.BaseURL)
				}
				if cfg.AI.AuthMode != "x-api-key" {
					t.Fatalf("AuthMode = %q", cfg.AI.AuthMode)
				}
				if cfg.AI.APIKey != "ant-key" {
					t.Fatalf("APIKey = %q, want AI_KEYS_JSON anthropic key", cfg.AI.APIKey)
				}
				if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "claude-sonnet" {
					t.Fatalf("models = %#v", cfg.AI.Models)
				}
			},
		},
		{
			name:    "yaml provider rejected when not in AI_ALLOWED_PROVIDERS",
			env:     map[string]string{"AI_PROVIDER": "openai"},
			yaml:    "review:\n  provider: anthropic\n  model: claude-sonnet\n",
			wantErr: "not in AI_PROVIDER",
		},
		{
			name:    "empty allowlist rejects yaml provider other than default",
			yaml:    "review:\n  provider: anthropic\n",
			wantErr: "not in AI_PROVIDER",
		},
		{
			name: "yaml cannot change BaseURL/privacy even if those keys appear",
			env: map[string]string{
				"AI_PROVIDER":               "openrouter,anthropic",
				"AI_MODEL":                  "provider/model",
				"OPENROUTER_PRIVACY_STRICT": "true",
			},
			yaml: `
review:
  provider: anthropic
  model: claude-sonnet
  base_url: https://evil.example.test
  api_key: stolen
  privacy_strict: false
  zdr: false
  data_collection: allow
  eu_routing: true
`,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "anthropic" {
					t.Fatalf("provider = %q", cfg.AI.Provider)
				}
				if cfg.AI.BaseURL != "https://api.anthropic.com" {
					t.Fatalf("yaml base_url leaked: %q", cfg.AI.BaseURL)
				}
				if cfg.AI.APIKey == "stolen" {
					t.Fatal("yaml api_key leaked into config")
				}
				if !cfg.OpenRouter.PrivacyStrict || !cfg.OpenRouter.ZDR || cfg.OpenRouter.DataCollection != "deny" {
					t.Fatalf("yaml privacy leaked: %#v", cfg.OpenRouter)
				}
				if cfg.OpenRouter.EURouting {
					t.Fatal("yaml eu_routing must not enable OpenRouter EU")
				}
			},
		},
		{
			name: "yaml openrouter-eu sets EU host with ZDR and no data retention",
			env: map[string]string{
				"AI_PROVIDER":                "openai,openrouter-eu",
				"OPENROUTER_PRIVACY_STRICT":  "false",
				"OPENROUTER_ZDR":             "false",
				"OPENROUTER_DATA_COLLECTION": "allow",
			},
			yaml: "review:\n  provider: openrouter-eu\n  model: provider/model\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "openrouter" {
					t.Fatalf("provider = %q, want openrouter", cfg.AI.Provider)
				}
				if !cfg.OpenRouter.EURouting || cfg.AI.BaseURL != "https://eu.openrouter.ai/api/v1" {
					t.Fatalf("EU host missing: url=%q eu=%t", cfg.AI.BaseURL, cfg.OpenRouter.EURouting)
				}
				if !cfg.OpenRouter.ZDR || cfg.OpenRouter.DataCollection != "deny" {
					t.Fatalf("EU privacy missing: %#v", cfg.OpenRouter)
				}
			},
		},
		{
			name:    "yaml openrouter-eu rejected when only global openrouter is allowlisted",
			env:     map[string]string{"AI_PROVIDER": "openrouter"},
			yaml:    "review:\n  provider: openrouter-eu\n  model: provider/model\n",
			wantErr: "not in AI_PROVIDER",
		},
		{
			name: "yaml can select allowlisted custom openai provider",
			env: map[string]string{
				"AI_PROVIDER":  "openai,vllm",
				"AI_KEYS_JSON": `{"openai":"openai-key","vllm":{"key":"local-key","base_url":"http://vllm:8000/v1"}}`,
			},
			yaml: "review:\n  provider: vllm\n  model: local-model\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "vllm" || cfg.AI.BaseURL != "http://vllm:8000/v1" {
					t.Fatalf("custom provider = %#v", cfg.AI)
				}
				if cfg.AI.APIKey != "local-key" {
					t.Fatalf("APIKey = %q", cfg.AI.APIKey)
				}
				if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "local-model" {
					t.Fatalf("models = %#v", cfg.AI.Models)
				}
			},
		},
		{
			name: "yaml cannot set custom base_url",
			env: map[string]string{
				"AI_PROVIDER":  "openai,vllm",
				"AI_KEYS_JSON": `{"openai":"openai-key","vllm":{"key":"local-key","base_url":"http://vllm:8000/v1"}}`,
			},
			yaml: "review:\n  provider: vllm\n  model: local-model\n  base_url: http://evil.example\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.BaseURL != "http://vllm:8000/v1" {
					t.Fatalf("yaml base_url leaked: %q", cfg.AI.BaseURL)
				}
			},
		},
		{
			name: "yaml model overrides AI_MODEL",
			env:  map[string]string{"AI_MODEL": "instance-model"},
			yaml: "review:\n  model: project-model\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "project-model" {
					t.Fatalf("models = %#v, want [project-model]", cfg.AI.Models)
				}
			},
		},
		{
			name: "yaml models wins over model",
			env:  map[string]string{"AI_MODEL": "instance-model"},
			yaml: `
review:
  model: ignored-model
  models: ["first-model", "second-model"]
`,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if len(cfg.AI.Models) != 2 || cfg.AI.Models[0] != "first-model" || cfg.AI.Models[1] != "second-model" {
					t.Fatalf("models = %#v", cfg.AI.Models)
				}
			},
		},
		{
			name: "AI_KEYS_JSON used for anthropic when selected",
			env: map[string]string{
				"AI_API_KEY":   "openai-key",
				"AI_PROVIDER":  "openai,anthropic",
				"AI_KEYS_JSON": `{"anthropic":"ant-from-json"}`,
			},
			yaml: "review:\n  provider: anthropic\n  model: claude-sonnet\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "anthropic" || cfg.AI.APIKey != "ant-from-json" {
					t.Fatalf("provider=%q key=%q", cfg.AI.Provider, cfg.AI.APIKey)
				}
			},
		},
		{
			name: "openrouter to xai resets default host",
			env: map[string]string{
				"AI_PROVIDER": "openrouter,xai",
				"AI_MODEL":    "provider/model",
				"AI_API_KEY":  "xai-api-key",
			},
			yaml: "review:\n  provider: xai\n  model: grok-4.6\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "xai" {
					t.Fatalf("provider = %q, want xai", cfg.AI.Provider)
				}
				if cfg.AI.BaseURL != "https://api.x.ai/v1" {
					t.Fatalf("BaseURL = %q, want api.x.ai (not leftover openrouter)", cfg.AI.BaseURL)
				}
				if cfg.AI.APIKey != "xai-api-key" {
					t.Fatalf("xai must use AI_API_KEY, got %q", cfg.AI.APIKey)
				}
			},
		},
		{
			name: "empty allowlist allows yaml default provider",
			yaml: "review:\n  provider: openai\n  model: project-model\n",
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.AI.Provider != "openai" {
					t.Fatalf("provider = %q", cfg.AI.Provider)
				}
				if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "project-model" {
					t.Fatalf("models = %#v", cfg.AI.Models)
				}
			},
		},
		{
			name:    "empty yaml model fails closed",
			yaml:    "review:\n  model: \"  \"\n",
			wantErr: "review.model",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBaseConfigEnv(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			file, err := ParseRepoFile([]byte(test.yaml))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ApplyRepoFile(file)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ApplyRepoFile() error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if test.check != nil {
				test.check(t, cfg)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
