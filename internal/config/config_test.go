package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProviderAliasesAndStrictPrivacy(t *testing.T) {
	tests := []struct {
		alias    string
		provider string
		baseURL  string
		eu       bool
	}{
		{"or-eu", "openrouter", "https://eu.openrouter.ai/api/v1", true},
		{"openrouter-global", "openrouter", "https://openrouter.ai/api/v1", false},
		{"openai-compatible", "openai", "https://api.openai.com/v1", false},
		{"anthropic-compat", "anthropic", "https://api.anthropic.com", false},
		{"grok", "grok", "https://api.x.ai/v1", false},
		{"grok-build", "grok", "https://api.x.ai/v1", false},
		{"xai", "xai", "https://api.x.ai/v1", false},
		{"codex", "codex", "https://api.openai.com/v1", false},
		{"chatgpt", "codex", "https://api.openai.com/v1", false},
		{"openai-codex", "codex", "https://api.openai.com/v1", false},
	}
	for _, test := range tests {
		t.Run(test.alias, func(t *testing.T) {
			cfg := Config{AI: AIConfig{Provider: test.alias}, OpenRouter: OpenRouterConfig{PrivacyStrict: true, ZDR: false, DataCollection: "allow"}}
			applyProviderDefaults(&cfg)
			if cfg.AI.Provider != test.provider || cfg.AI.BaseURL != test.baseURL || cfg.OpenRouter.EURouting != test.eu {
				t.Fatalf("alias normalization = provider %q base %q eu %t", cfg.AI.Provider, cfg.AI.BaseURL, cfg.OpenRouter.EURouting)
			}
			if cfg.AI.Provider == "openrouter" && (!cfg.OpenRouter.ZDR || cfg.OpenRouter.DataCollection != "deny") {
				t.Fatalf("strict privacy was not enforced: %#v", cfg.OpenRouter)
			}
		})
	}
}

func TestMentionAliasesAreNormalizedAndDeduplicated(t *testing.T) {
	cfg := Config{GitLab: GitLabConfig{BotUsername: "@CodeReview-Bot"}, Review: ReviewConfig{Mention: "@CodeReview", MentionAliases: []string{"CR", "codereview"}}}
	applyMentionAliases(&cfg)
	want := []string{"codereview", "codereview-bot", "cr"}
	if len(cfg.Review.MentionAliases) != len(want) {
		t.Fatalf("aliases = %#v", cfg.Review.MentionAliases)
	}
	for index := range want {
		if cfg.Review.MentionAliases[index] != want[index] {
			t.Fatalf("aliases = %#v", cfg.Review.MentionAliases)
		}
	}
}

func TestLoadUsesCIVariables(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("CI_API_V4_URL", "https://gitlab.example.test/api/v4")
	t.Setenv("GITLAB_API_URL", "")
	t.Setenv("CI_JOB_TOKEN", "job-token")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("CI_PROJECT_ID", "42")
	t.Setenv("CI_MERGE_REQUEST_IID", "7")
	t.Setenv("REVIEW_MODE", "deep")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitLab.APIURL != "https://gitlab.example.test/api/v4" {
		t.Fatalf("API URL = %q", cfg.GitLab.APIURL)
	}
	if cfg.GitLab.Token != "job-token" || !cfg.GitLab.UseJobToken {
		t.Fatalf("token = %q useJobToken=%t", cfg.GitLab.Token, cfg.GitLab.UseJobToken)
	}
	if cfg.Target.ProjectID != 42 || cfg.Target.MergeRequest != 7 || cfg.Target.Mode != "deep" {
		t.Fatalf("target = %#v", cfg.Target)
	}
	if err := cfg.ValidateTarget(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPrefersGitLabTokenOverJobToken(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("GITLAB_TOKEN", "project-access-token")
	t.Setenv("CI_JOB_TOKEN", "job-token")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitLab.Token != "project-access-token" || cfg.GitLab.UseJobToken {
		t.Fatalf("token = %q useJobToken=%t", cfg.GitLab.Token, cfg.GitLab.UseJobToken)
	}
}

func TestLoadStrictPrivacyOverridesContradictoryEnvironment(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "or-eu")
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_MODEL", "provider/model")
	t.Setenv("OPENROUTER_PRIVACY_STRICT", "true")
	t.Setenv("OPENROUTER_ZDR", "false")
	t.Setenv("OPENROUTER_DATA_COLLECTION", "allow")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "openrouter" || cfg.AI.BaseURL != "https://eu.openrouter.ai/api/v1" || !cfg.OpenRouter.EURouting {
		t.Fatalf("unexpected EU provider configuration: %#v %#v", cfg.AI, cfg.OpenRouter)
	}
	if !cfg.OpenRouter.ZDR || cfg.OpenRouter.DataCollection != "deny" {
		t.Fatalf("strict privacy was downgraded: %#v", cfg.OpenRouter)
	}
}

func TestLoadEURoutingForcesZDRAndNoDataRetention(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "openrouter-eu")
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_MODEL", "provider/model")
	t.Setenv("OPENROUTER_PRIVACY_STRICT", "false")
	t.Setenv("OPENROUTER_ZDR", "false")
	t.Setenv("OPENROUTER_DATA_COLLECTION", "allow")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "openrouter" || cfg.AI.BaseURL != "https://eu.openrouter.ai/api/v1" || !cfg.OpenRouter.EURouting {
		t.Fatalf("EU routing = %#v %#v", cfg.AI, cfg.OpenRouter)
	}
	if !cfg.OpenRouter.ZDR || cfg.OpenRouter.DataCollection != "deny" {
		t.Fatalf("EU mode must force ZDR and data_collection=deny: %#v", cfg.OpenRouter)
	}
}

func TestLoadRejectsAbsoluteAIPaths(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("OPENAI_CHAT_COMPLETIONS_PATH", "https://evil.example.test/chat/completions")
	if _, err := Load(); err == nil {
		t.Fatal("expected absolute AI path to be rejected")
	}
}

func TestLoadRejectsNegativeBounds(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_MAX_RETRIES", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected negative bounds to be rejected")
	}
}

func TestLoadRejectsMalformedScalarEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "invalid bool", env: map[string]string{"BLOCK_ON_FINDINGS": "maybe"}, want: "BLOCK_ON_FINDINGS"},
		{name: "invalid int", env: map[string]string{"MAX_DIFF_FILES": "abc"}, want: "MAX_DIFF_FILES"},
		{name: "invalid float", env: map[string]string{"AI_TEMPERATURE": "bogus"}, want: "AI_TEMPERATURE"},
		{name: "invalid duration", env: map[string]string{"AI_TIMEOUT": "tomorrow"}, want: "AI_TIMEOUT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBaseConfigEnv(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsNegativeSafetyLimits(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "negative temperature", env: map[string]string{"AI_TEMPERATURE": "-0.1"}, want: "AI_TEMPERATURE"},
		{name: "zero input bound", env: map[string]string{"AI_MAX_INPUT_CHARS": "0"}, want: "AI_MAX_INPUT_CHARS"},
		{name: "negative related context", env: map[string]string{"MAX_RELATED_CONTEXT_FILES": "-1"}, want: "MAX_RELATED_CONTEXT_FILES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBaseConfigEnv(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateTargetRequiresIdentity(t *testing.T) {
	cfg := Config{}
	if err := cfg.ValidateTarget(); err == nil {
		t.Fatal("expected missing target error")
	}
	cfg.Target.ProjectID = 1
	if err := cfg.ValidateTarget(); err == nil {
		t.Fatal("expected missing MR error")
	}
	cfg.Target.MergeRequest = 2
	if err := cfg.ValidateTarget(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTargetOverrides(t *testing.T) {
	cfg := Config{Target: TargetConfig{ProjectID: 1, MergeRequest: 2, Mode: "quick"}}
	cfg.ApplyTargetOverrides(9, 8, "deep", true, true, true, true, true, true, true, true)
	if cfg.Target.ProjectID != 9 || cfg.Target.MergeRequest != 8 || cfg.Target.Mode != "deep" || !cfg.Target.Full || !cfg.Target.Force || !cfg.Target.SummaryOnly || !cfg.Target.DryRun {
		t.Fatalf("overrides = %#v", cfg.Target)
	}
}

func TestLoadRuntimeReliabilitySettings(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		setBaseConfigEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.JobTimeout != 20*time.Minute {
			t.Fatalf("JOB_TIMEOUT default = %s", cfg.JobTimeout)
		}
		if cfg.GitLab.MaxRetries != 2 {
			t.Fatalf("GITLAB_MAX_RETRIES default = %d", cfg.GitLab.MaxRetries)
		}
		if cfg.AI.MaxCompletions != 8 {
			t.Fatalf("AI_MAX_COMPLETIONS default = %d", cfg.AI.MaxCompletions)
		}
		if cfg.Target.DryRun {
			t.Fatal("REVIEW_DRY_RUN default must be false")
		}
		if cfg.Review.ReviewDrafts {
			t.Fatal("REVIEW_DRAFTS default must be false")
		}
		if cfg.Review.SummaryInDescription {
			t.Fatal("SUMMARY_IN_DESCRIPTION default must be false")
		}
		if cfg.Review.ApproveOnClean {
			t.Fatal("GITLAB_APPROVE_ON_CLEAN default must be false")
		}
		if cfg.Review.DependencyBumps {
			t.Fatal("REVIEW_DEPENDENCY_BUMPS default must be false")
		}
		if cfg.Review.ApproveDependencyBumps {
			t.Fatal("GITLAB_APPROVE_DEPENDENCY_BUMPS default must be false")
		}
		if cfg.Review.RequestReview {
			t.Fatal("GITLAB_REQUEST_REVIEW default must be false")
		}
		if cfg.Review.TodoMax != 5 {
			t.Fatalf("TODO_MAX default = %d", cfg.Review.TodoMax)
		}
		if cfg.Review.SkipAck {
			t.Fatal("SKIP_MR_ACK default must be false")
		}
		if !containsString(cfg.Review.SkipTokens, "skip-codereview") || !containsString(cfg.Review.SkipTokens, "[skip review]") {
			t.Fatalf("SKIP_MR_TOKENS default = %#v", cfg.Review.SkipTokens)
		}
		if !containsString(cfg.Review.RepositoryGuidePatterns, "AI-Standard.md") {
			t.Fatalf("REPOSITORY_GUIDELINE_PATTERNS must include AI-Standard.md, got %#v", cfg.Review.RepositoryGuidePatterns)
		}
	})

	t.Run("valid values", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("JOB_TIMEOUT", "5m")
		t.Setenv("GITLAB_MAX_RETRIES", "0")
		t.Setenv("AI_MAX_COMPLETIONS", "1")
		t.Setenv("REVIEW_DRY_RUN", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.JobTimeout != 5*time.Minute {
			t.Fatalf("JOB_TIMEOUT = %s", cfg.JobTimeout)
		}
		if cfg.GitLab.MaxRetries != 0 {
			t.Fatalf("GITLAB_MAX_RETRIES = %d", cfg.GitLab.MaxRetries)
		}
		if cfg.AI.MaxCompletions != 1 {
			t.Fatalf("AI_MAX_COMPLETIONS = %d", cfg.AI.MaxCompletions)
		}
		if !cfg.Target.DryRun {
			t.Fatal("REVIEW_DRY_RUN = false")
		}
	})

	t.Run("empty uses defaults", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("JOB_TIMEOUT", "")
		t.Setenv("GITLAB_MAX_RETRIES", "")
		t.Setenv("AI_MAX_COMPLETIONS", "")
		t.Setenv("REVIEW_DRY_RUN", "")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.JobTimeout != 20*time.Minute || cfg.GitLab.MaxRetries != 2 || cfg.AI.MaxCompletions != 8 || cfg.Target.DryRun {
			t.Fatalf("empty env did not keep defaults: timeout=%s retries=%d completions=%d dryRun=%t", cfg.JobTimeout, cfg.GitLab.MaxRetries, cfg.AI.MaxCompletions, cfg.Target.DryRun)
		}
		if cfg.Review.ReviewDrafts || cfg.Review.SummaryInDescription {
			t.Fatalf("empty env did not keep safer defaults: drafts=%t summaryInDescription=%t", cfg.Review.ReviewDrafts, cfg.Review.SummaryInDescription)
		}
	})
}

func TestLoadSaferReviewDefaults(t *testing.T) {
	t.Run("empty uses defaults", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DRAFTS", "")
		t.Setenv("AUTO_REVIEW_DRAFTS", "")
		t.Setenv("SUMMARY_IN_DESCRIPTION", "")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.ReviewDrafts || cfg.Review.SummaryInDescription {
			t.Fatalf("empty env did not keep safer defaults: drafts=%t summaryInDescription=%t", cfg.Review.ReviewDrafts, cfg.Review.SummaryInDescription)
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DRAFTS", "true")
		t.Setenv("SUMMARY_IN_DESCRIPTION", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Review.ReviewDrafts || !cfg.Review.SummaryInDescription {
			t.Fatalf("explicit true not honored: drafts=%t summaryInDescription=%t", cfg.Review.ReviewDrafts, cfg.Review.SummaryInDescription)
		}
	})

	t.Run("auto review drafts alias", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DRAFTS", "")
		t.Setenv("AUTO_REVIEW_DRAFTS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Review.ReviewDrafts {
			t.Fatal("AUTO_REVIEW_DRAFTS=true should enable draft reviews")
		}
	})

	t.Run("malformed review drafts", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DRAFTS", "maybe")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REVIEW_DRAFTS") {
			t.Fatalf("Load() error = %v, want REVIEW_DRAFTS", err)
		}
	})

	t.Run("malformed summary in description", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("SUMMARY_IN_DESCRIPTION", "maybe")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SUMMARY_IN_DESCRIPTION") {
			t.Fatalf("Load() error = %v, want SUMMARY_IN_DESCRIPTION", err)
		}
	})

	t.Run("malformed auto review drafts", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("AUTO_REVIEW_DRAFTS", "maybe")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AUTO_REVIEW_DRAFTS") {
			t.Fatalf("Load() error = %v, want AUTO_REVIEW_DRAFTS", err)
		}
	})
}

func TestLoadDependencyBumpSettings(t *testing.T) {
	t.Run("defaults false when unset", func(t *testing.T) {
		setBaseConfigEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DependencyBumps || cfg.Review.ApproveDependencyBumps {
			t.Fatalf("dependency bump defaults must be false: bumps=%t approve=%t", cfg.Review.DependencyBumps, cfg.Review.ApproveDependencyBumps)
		}
	})

	t.Run("empty uses defaults", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMPS", "")
		t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DependencyBumps || cfg.Review.ApproveDependencyBumps {
			t.Fatalf("empty env did not keep safer defaults: bumps=%t approve=%t", cfg.Review.DependencyBumps, cfg.Review.ApproveDependencyBumps)
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMPS", "true")
		t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Review.DependencyBumps || !cfg.Review.ApproveDependencyBumps {
			t.Fatalf("explicit true not honored: bumps=%t approve=%t", cfg.Review.DependencyBumps, cfg.Review.ApproveDependencyBumps)
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMPS", "false")
		t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "false")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DependencyBumps || cfg.Review.ApproveDependencyBumps {
			t.Fatalf("explicit false not honored: bumps=%t approve=%t", cfg.Review.DependencyBumps, cfg.Review.ApproveDependencyBumps)
		}
	})

	t.Run("malformed review dependency bumps", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMPS", "maybe")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REVIEW_DEPENDENCY_BUMPS") {
			t.Fatalf("Load() error = %v, want REVIEW_DEPENDENCY_BUMPS", err)
		}
	})

	t.Run("malformed approve dependency bumps", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("GITLAB_APPROVE_DEPENDENCY_BUMPS", "maybe")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "GITLAB_APPROVE_DEPENDENCY_BUMPS") {
			t.Fatalf("Load() error = %v, want GITLAB_APPROVE_DEPENDENCY_BUMPS", err)
		}
	})
}

func TestLoadDependencyBumpMode(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		setBaseConfigEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DependencyBumpMode != "" {
			t.Fatalf("default mode = %q, want empty", cfg.Review.DependencyBumpMode)
		}
		if cfg.Review.BumpPushURL != "" || cfg.Review.BumpPushSecret != "" {
			t.Fatalf("PUSH defaults must be empty: url=%q secret set=%t", cfg.Review.BumpPushURL, cfg.Review.BumpPushSecret != "")
		}
	})
	t.Run("each named mode", func(t *testing.T) {
		for _, mode := range []string{"notify", "wait", "validate"} {
			setBaseConfigEnv(t)
			t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", mode)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if cfg.Review.DependencyBumpMode != mode {
				t.Fatalf("mode %s: got %q", mode, cfg.Review.DependencyBumpMode)
			}
		}
	})
	t.Run("PUSH URL is not a mode", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("DEPENDENCY_BUMP_PUSH_URL", "https://agent.example.test/hook")
		t.Setenv("DEPENDENCY_BUMP_PUSH_SECRET", "push-secret")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Review.DependencyBumpMode != "" {
			t.Fatalf("mode = %q, want empty", cfg.Review.DependencyBumpMode)
		}
		if cfg.Review.BumpPushURL != "https://agent.example.test/hook" {
			t.Fatalf("PUSH URL = %q", cfg.Review.BumpPushURL)
		}
		if cfg.Review.BumpPushSecret != "push-secret" {
			t.Fatalf("PUSH secret = %q", cfg.Review.BumpPushSecret)
		}
	})
	t.Run("removed modes fail closed", func(t *testing.T) {
		for _, mode := range []string{"agent_report", "agent-report", "push"} {
			setBaseConfigEnv(t)
			t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", mode)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REVIEW_DEPENDENCY_BUMP_MODE") {
				t.Fatalf("mode %s: Load() error = %v", mode, err)
			}
		}
	})
	t.Run("off aliases", func(t *testing.T) {
		for _, raw := range []string{"off", "none", "disabled"} {
			setBaseConfigEnv(t)
			t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", raw)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("%s: %v", raw, err)
			}
			if cfg.Review.DependencyBumpMode != "" {
				t.Fatalf("%s: got %q", raw, cfg.Review.DependencyBumpMode)
			}
		}
	})
	t.Run("invalid mode fails closed", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "automerge")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "REVIEW_DEPENDENCY_BUMP_MODE") {
			t.Fatalf("Load() error = %v, want REVIEW_DEPENDENCY_BUMP_MODE", err)
		}
	})
	t.Run("invalid PUSH URL fails closed", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("DEPENDENCY_BUMP_PUSH_URL", "not-a-url")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DEPENDENCY_BUMP_PUSH_URL") {
			t.Fatalf("Load() error = %v, want DEPENDENCY_BUMP_PUSH_URL", err)
		}
	})
}

func TestLoadRejectsRuntimeReliabilityBounds(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "zero job timeout", env: map[string]string{"JOB_TIMEOUT": "0s"}, want: "JOB_TIMEOUT"},
		{name: "negative job timeout", env: map[string]string{"JOB_TIMEOUT": "-1s"}, want: "JOB_TIMEOUT"},
		{name: "malformed job timeout", env: map[string]string{"JOB_TIMEOUT": "tomorrow"}, want: "JOB_TIMEOUT"},
		{name: "negative gitlab retries", env: map[string]string{"GITLAB_MAX_RETRIES": "-1"}, want: "GITLAB_MAX_RETRIES"},
		{name: "malformed gitlab retries", env: map[string]string{"GITLAB_MAX_RETRIES": "abc"}, want: "GITLAB_MAX_RETRIES"},
		{name: "zero completions", env: map[string]string{"AI_MAX_COMPLETIONS": "0"}, want: "AI_MAX_COMPLETIONS"},
		{name: "negative completions", env: map[string]string{"AI_MAX_COMPLETIONS": "-2"}, want: "AI_MAX_COMPLETIONS"},
		{name: "malformed completions", env: map[string]string{"AI_MAX_COMPLETIONS": "nope"}, want: "AI_MAX_COMPLETIONS"},
		{name: "malformed dry run", env: map[string]string{"REVIEW_DRY_RUN": "maybe"}, want: "REVIEW_DRY_RUN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBaseConfigEnv(t)
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func setBaseConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEREVIEW_AUTH_PATH", filepath.Join(t.TempDir(), "auth.json"))
	t.Setenv("CI", "true")
	t.Setenv("GITLAB_CI", "true")
	t.Setenv("CODEREVIEW_USE_LOCAL_CREDENTIALS", "false")
	t.Setenv("GITLAB_API_URL", "https://gitlab.example.test/api/v4")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("CI_API_V4_URL", "")
	t.Setenv("CI_JOB_TOKEN", "")
	t.Setenv("CI_PROJECT_ID", "")
	t.Setenv("CI_MERGE_REQUEST_IID", "")
	t.Setenv("PROJECT_ID", "")
	t.Setenv("MERGE_REQUEST_IID", "")
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_API_KEY", "key")
	t.Setenv("AI_MODEL", "model")
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("OPENAI_CHAT_COMPLETIONS_PATH", "/chat/completions")
	t.Setenv("AI_AUTH_MODE", "")
	t.Setenv("AI_KEYS_JSON", "")
	t.Setenv("AI_KEYS_FILE", "")
	t.Setenv("AI_ALLOWED_PROVIDERS", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_TOKEN", "")
	t.Setenv("REVIEW_DEPENDENCY_BUMP_MODE", "")
	t.Setenv("DEPENDENCY_BUMP_PUSH_URL", "")
	t.Setenv("DEPENDENCY_BUMP_PUSH_SECRET", "")
}

func TestLoadSkipMRTokensAndAck(t *testing.T) {
	t.Run("custom tokens and ack", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("SKIP_MR_TOKENS", "wip, [skip ci]")
		t.Setenv("SKIP_MR_ACK", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !containsString(cfg.Review.SkipTokens, "wip") || !containsString(cfg.Review.SkipTokens, "[skip ci]") {
			t.Fatalf("SkipTokens = %#v", cfg.Review.SkipTokens)
		}
		if !cfg.Review.SkipAck {
			t.Fatal("SKIP_MR_ACK=true was ignored")
		}
	})
	t.Run("empty tokens disable matching", func(t *testing.T) {
		setBaseConfigEnv(t)
		t.Setenv("SKIP_MR_TOKENS", "")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Review.SkipTokens) != 0 {
			t.Fatalf("empty SKIP_MR_TOKENS must disable tokens, got %#v", cfg.Review.SkipTokens)
		}
	})
}

func TestLoadXAIUsesAPIKeyAndDefaultModel(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "xai")
	t.Setenv("AI_API_KEY", "xai-test-key")
	t.Setenv("AI_MODEL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "xai" || cfg.AI.BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("xai config = %#v", cfg.AI)
	}
	if cfg.AI.APIKey != "xai-test-key" {
		t.Fatalf("API key = %q", cfg.AI.APIKey)
	}
	if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "grok-4.6" {
		t.Fatalf("models = %#v", cfg.AI.Models)
	}
}

func TestLoadGrokIsSubscriptionNotAPIKey(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "grok")
	t.Setenv("AI_API_KEY", "metered-xai-key")
	t.Setenv("AI_MODEL", "")
	if _, err := Load(); err == nil {
		t.Fatal("grok must not use AI_API_KEY; that key is the xai API path")
	}

	authPath := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("CODEREVIEW_AUTH_PATH", authPath)
	if err := os.WriteFile(authPath, []byte(`{"version":1,"providers":{"grok":{"access_token":"grok-oauth-token"}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Provider != "grok" || cfg.AI.BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("grok config = %#v", cfg.AI)
	}
	if cfg.AI.APIKey != "grok-oauth-token" {
		t.Fatalf("grok must use subscription oauth, got %q", cfg.AI.APIKey)
	}
	if len(cfg.AI.Models) != 1 || cfg.AI.Models[0] != "grok-4.6" {
		t.Fatalf("models = %#v", cfg.AI.Models)
	}
}

func TestLoadDoesNotUseProviderSpecificAPIKeyEnv(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "xai")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "")
	t.Setenv("XAI_API_KEY", "xai-test-key")
	if _, err := Load(); err == nil {
		t.Fatal("XAI_API_KEY must not satisfy AI credentials")
	}
}

func TestLoadRejectsMalformedAIKeysJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "invalid json", raw: "{not-json", want: "AI_KEYS_JSON"},
		{name: "array", raw: `["openai"]`, want: "AI_KEYS_JSON"},
		{name: "unknown provider", raw: `{"not-a-provider":"secret"}`, want: "AI_KEYS_JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setBaseConfigEnv(t)
			t.Setenv("AI_KEYS_JSON", test.raw)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadLegacyAllowedProvidersStillEnableNames(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_ALLOWED_PROVIDERS", "anthropic")
	t.Setenv("AI_KEYS_JSON", `{"anthropic":"ant-key"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectProvider("anthropic"); err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKey != "ant-key" {
		t.Fatalf("APIKey = %q", cfg.AI.APIKey)
	}
}

func TestLoadAIKeysFile(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":"file-openai-key","anthropic":"file-ant-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_PROVIDER", "openai,anthropic")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKey != "file-openai-key" {
		t.Fatalf("default provider key = %q, want file-openai-key", cfg.AI.APIKey)
	}
	if err := cfg.SelectProvider("anthropic"); err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKey != "file-ant-key" {
		t.Fatalf("anthropic key = %q, want file-ant-key", cfg.AI.APIKey)
	}
}

func TestLoadAIKeysJSONWinsOverFile(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_KEYS_JSON", `{"openai":"from-env"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.APIKey != "from-env" {
		t.Fatalf("APIKey = %q, want from-env", cfg.AI.APIKey)
	}
}

func TestLoadAIKeysFileCreatesMissing(t *testing.T) {
	setBaseConfigEnv(t)
	path := filepath.Join(t.TempDir(), "ai-keys.json")
	t.Setenv("AI_KEYS_FILE", path)
	t.Setenv("AI_PROVIDER", "openai,grok")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"openai"`) || !strings.Contains(string(raw), `"grok"`) {
		t.Fatalf("created keys file = %s", raw)
	}
}

func TestLoadAIKeysFileFailsClosed(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		setBaseConfigEnv(t)
		dir := t.TempDir()
		t.Setenv("AI_KEYS_FILE", dir)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AI_KEYS_FILE") {
			t.Fatalf("Load() error = %v, want AI_KEYS_FILE", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		setBaseConfigEnv(t)
		path := filepath.Join(t.TempDir(), "ai-keys.json")
		if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AI_KEYS_FILE", path)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AI_KEYS_FILE") {
			t.Fatalf("Load() error = %v, want AI_KEYS_FILE", err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		setBaseConfigEnv(t)
		path := filepath.Join(t.TempDir(), "ai-keys.json")
		if err := os.WriteFile(path, []byte(strings.Repeat("a", maxAIKeysFileBytes+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("AI_KEYS_FILE", path)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AI_KEYS_FILE") {
			t.Fatalf("Load() error = %v, want AI_KEYS_FILE", err)
		}
	})
}

func TestLoadDoesNotReadHomeCredentialsInCI(t *testing.T) {
	setBaseConfigEnv(t)
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_MODEL", "model")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"access_token":"home-secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("CI must not silently use ~/.codex/auth.json")
	}
}
