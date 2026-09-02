package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nicolaeser/codereview/internal/auth"
)

const maxAIKeysFileBytes = 64 << 10

// Config is the runtime configuration for a one-shot CI review.
type Config struct {
	JobTimeout   time.Duration
	GitLab       GitLabConfig
	AI           AIConfig
	OpenRouter   OpenRouterConfig
	Review       ReviewConfig
	Instructions InstructionsConfig
	State        StateConfig
	Target       TargetConfig
	LogLevel     string

	defaultProvider    string
	defaultAllowlistID string
	defaultModels      []string
	enabledProviders   []string
	providerKeys       map[string]string
	customProviders    map[string]customProvider
	keysPrivacy        map[string]keysPrivacyOverlay
	providerModels     map[string][]string
	baseURLFromEnv     bool
	authModeFromEnv    bool
	euRoutingFromEnv   bool
	modelsFromEnv      bool
}

type GitLabConfig struct {
	APIURL string
	Token  string
	// UseJobToken selects the JOB-TOKEN header (CI_JOB_TOKEN) instead of PRIVATE-TOKEN.
	UseJobToken bool
	BotUsername string
	Timeout     time.Duration
	MaxRetries  int
}

type AIConfig struct {
	Provider              string
	BaseURL               string
	APIKey                string
	Models                []string
	AuthMode              string
	APIKeyHeader          string
	ChatCompletionsPath   string
	AnthropicMessagesPath string
	AnthropicVersion      string
	MaxTokens             int
	MaxInputChars         int
	Temperature           float64
	Timeout               time.Duration
	MaxRetries            int
	MaxCompletions        int
	JSONMode              string
	JSONRepair            bool
	MaxTokensField        string
	ExtraHeaders          map[string]string
}

type OpenRouterConfig struct {
	EURouting             bool
	PrivacyStrict         bool
	ZDR                   bool
	DataCollection        string
	IncludeZDR            bool
	IncludeDataCollection bool
	AllowFallbacks        bool
	RequireParameters     bool
	ProviderOrder         []string
	OnlyProviders         []string
	IgnoredProviders      []string
	SiteURL               string
	AppName               string
	RouterMetadata        bool
}

type ReviewConfig struct {
	Mention                   string
	MentionAliases            []string
	DefaultMode               string
	DeepVerification          bool
	CommentStyle              string
	ReviewDrafts              bool
	DependencyBumps           bool
	AutoIncremental           bool
	IgnoreAuthors             []string
	IgnorePaths               []string
	ContextFiles              []string
	MaxDiffFiles              int
	MaxDiffChars              int
	MaxFileContentChars       int
	MaxContextFiles           int
	MaxTreeEntries            int
	MaxComments               int
	MinimumConfidence         float64
	PostProgress              bool
	PostSummary               bool
	SummaryInDescription      bool
	PostInline                bool
	CommitStatus              bool
	StatusReviewModes         []string
	BlockOnFindings           bool
	BlockingSeverities        []string
	BlockingReviewModes       []string
	ReviewStatusName          string
	IncludeLinkedIssues       bool
	IncludeRepositoryTree     bool
	IncludeChangedFileContent bool
	IncludeRepositoryGuides   bool
	RepositoryGuidePatterns   []string
	IncludeRelatedFiles       bool
	MaxRelatedContextFiles    int
	PathInstructions          []PathInstruction
	ApproveOnClean            bool
	ApproveDependencyBumps    bool
	DependencyBumpMode        string
	BumpPushURL               string
	BumpPushSecret            string
	RequestReview             bool
	TodoMax                   int
	SkipTokens                []string
	SkipAck                   bool
}

type PathInstruction struct {
	Path         string `json:"path" yaml:"path"`
	Instructions string `json:"instructions" yaml:"instructions"`
}

type InstructionsConfig struct {
	DefaultPath    string
	AdditionalPath string
}

type StateConfig struct {
	Path string
}

// TargetConfig identifies the merge request under review for this process.
type TargetConfig struct {
	ProjectID    int64
	MergeRequest int64
	Mode         string
	Full         bool
	Force        bool
	SummaryOnly  bool
	DryRun       bool
}

func Load() (Config, error) {
	cfg := Config{
		LogLevel:   strings.ToLower(env("LOG_LEVEL", "info")),
		JobTimeout: 20 * time.Minute,
		GitLab: GitLabConfig{
			APIURL:      strings.TrimRight(firstNonEmpty(os.Getenv("GITLAB_API_URL"), os.Getenv("CI_API_V4_URL")), "/"),
			Token:       firstNonEmpty(os.Getenv("GITLAB_TOKEN"), os.Getenv("CI_JOB_TOKEN")),
			UseJobToken: strings.TrimSpace(os.Getenv("GITLAB_TOKEN")) == "" && strings.TrimSpace(os.Getenv("CI_JOB_TOKEN")) != "",
			BotUsername: strings.TrimPrefix(env("GITLAB_BOT_USERNAME", "codereview"), "@"),
			Timeout:     30 * time.Second,
			MaxRetries:  2,
		},
		AI: AIConfig{
			Provider:              strings.ToLower(env("AI_PROVIDER", "openai")),
			BaseURL:               strings.TrimRight(os.Getenv("AI_BASE_URL"), "/"),
			APIKey:                os.Getenv("AI_API_KEY"),
			Models:                envCSV("AI_MODELS", env("AI_MODEL", "")),
			AuthMode:              strings.ToLower(os.Getenv("AI_AUTH_MODE")),
			APIKeyHeader:          env("AI_API_KEY_HEADER", "api-key"),
			ChatCompletionsPath:   env("OPENAI_CHAT_COMPLETIONS_PATH", "/chat/completions"),
			AnthropicMessagesPath: env("ANTHROPIC_MESSAGES_PATH", "/v1/messages"),
			AnthropicVersion:      env("ANTHROPIC_VERSION", "2023-06-01"),
			MaxTokens:             6000,
			MaxInputChars:         90000,
			Temperature:           0.1,
			Timeout:               180 * time.Second,
			MaxRetries:            2,
			MaxCompletions:        8,
			JSONMode:              strings.ToLower(env("AI_JSON_MODE", "prompt")),
			JSONRepair:            true,
			MaxTokensField:        env("OPENAI_MAX_TOKENS_FIELD", "max_tokens"),
			ExtraHeaders:          map[string]string{},
		},
		OpenRouter: OpenRouterConfig{
			EURouting:             false,
			PrivacyStrict:         true,
			ZDR:                   true,
			DataCollection:        strings.ToLower(env("OPENROUTER_DATA_COLLECTION", "deny")),
			IncludeZDR:            true,
			IncludeDataCollection: true,
			AllowFallbacks:        true,
			RequireParameters:     false,
			ProviderOrder:         envCSV("OPENROUTER_PROVIDER_ORDER", ""),
			OnlyProviders:         envCSV("OPENROUTER_ONLY_PROVIDERS", ""),
			IgnoredProviders:      envCSV("OPENROUTER_IGNORE_PROVIDERS", ""),
			SiteURL:               os.Getenv("OPENROUTER_SITE_URL"),
			AppName:               env("OPENROUTER_APP_NAME", "CodeReview"),
			RouterMetadata:        false,
		},
		Review: ReviewConfig{
			Mention:                   strings.TrimPrefix(env("BOT_MENTION", "codereview"), "@"),
			MentionAliases:            envCSV("BOT_MENTION_ALIASES", "codereview-bot,cr"),
			DefaultMode:               strings.ToLower(env("DEFAULT_REVIEW_MODE", "standard")),
			DeepVerification:          true,
			CommentStyle:              strings.ToLower(env("COMMENT_STYLE", "compact")),
			ReviewDrafts:              false,
			DependencyBumps:           false,
			AutoIncremental:           true,
			IgnoreAuthors:             envCSV("IGNORE_AUTHORS", ""),
			IgnorePaths:               envCSV("IGNORE_PATHS", "vendor/**,node_modules/**,dist/**,build/**,coverage/**,*.lock,go.sum"),
			ContextFiles:              envCSV("CONTEXT_FILES", "README.md,go.mod,package.json,pyproject.toml,Cargo.toml,Makefile,.gitlab-ci.yml"),
			MaxDiffFiles:              100,
			MaxDiffChars:              180000,
			MaxFileContentChars:       30000,
			MaxContextFiles:           24,
			MaxTreeEntries:            500,
			MaxComments:               12,
			MinimumConfidence:         0.78,
			PostProgress:              true,
			PostSummary:               true,
			SummaryInDescription:      false,
			PostInline:                true,
			CommitStatus:              false,
			StatusReviewModes:         lowerValues(envCSV("STATUS_REVIEW_MODES", "standard,deep,security")),
			BlockOnFindings:           false,
			BlockingSeverities:        envCSV("BLOCKING_SEVERITIES", "critical,high"),
			BlockingReviewModes:       lowerValues(envCSV("BLOCKING_REVIEW_MODES", "standard,deep,security")),
			ReviewStatusName:          env("GITLAB_COMMIT_STATUS_NAME", "CodeReview"),
			IncludeLinkedIssues:       true,
			IncludeRepositoryTree:     true,
			IncludeChangedFileContent: true,
			IncludeRepositoryGuides:   true,
			RepositoryGuidePatterns:   envCSV("REPOSITORY_GUIDELINE_PATTERNS", "AGENTS.md,**/AGENTS.md,CLAUDE.md,**/CLAUDE.md,AI-Standard.md,**/AI-Standard.md,.cursorrules,**/.cursorrules,.github/copilot-instructions.md,.github/instructions/*.instructions.md"),
			IncludeRelatedFiles:       true,
			MaxRelatedContextFiles:    8,
			ApproveOnClean:            false,
			ApproveDependencyBumps:    false,
			DependencyBumpMode:        "",
			RequestReview:             false,
			TodoMax:                   5,
			SkipTokens:                envCSV("SKIP_MR_TOKENS", "skip-codereview,codereview-skip,no-codereview,[skip review],[skip-codereview]"),
			SkipAck:                   false,
		},
		Instructions: InstructionsConfig{
			DefaultPath:    env("INSTRUCTION_PATH", "INSTRUCTION.md"),
			AdditionalPath: env("INSTRUCTION_ADDITIONAL_PATH", "INSTRUCTION-ADDITIONAL.md"),
		},
		State: StateConfig{
			Path: env("STATE_PATH", "data/state.json"),
		},
		Target: TargetConfig{
			Mode: strings.ToLower(env("REVIEW_MODE", "")),
		},
		baseURLFromEnv:   strings.TrimSpace(os.Getenv("AI_BASE_URL")) != "",
		authModeFromEnv:  strings.TrimSpace(os.Getenv("AI_AUTH_MODE")) != "",
		euRoutingFromEnv: envIsSet("OPENROUTER_EU_ROUTING"),
		modelsFromEnv:    envIsSet("AI_MODEL") || envIsSet("AI_MODELS"),
	}

	var err error
	if cfg.JobTimeout, err = envDuration("JOB_TIMEOUT", cfg.JobTimeout); err != nil {
		return Config{}, err
	}
	if cfg.GitLab.Timeout, err = envDuration("GITLAB_TIMEOUT", cfg.GitLab.Timeout); err != nil {
		return Config{}, err
	}
	if cfg.GitLab.MaxRetries, err = envInt("GITLAB_MAX_RETRIES", cfg.GitLab.MaxRetries); err != nil {
		return Config{}, err
	}
	if cfg.AI.MaxTokens, err = envInt("AI_MAX_TOKENS", cfg.AI.MaxTokens); err != nil {
		return Config{}, err
	}
	if cfg.AI.MaxInputChars, err = envInt("AI_MAX_INPUT_CHARS", cfg.AI.MaxInputChars); err != nil {
		return Config{}, err
	}
	if cfg.AI.Temperature, err = envFloat("AI_TEMPERATURE", cfg.AI.Temperature); err != nil {
		return Config{}, err
	}
	if cfg.AI.Timeout, err = envDuration("AI_TIMEOUT", cfg.AI.Timeout); err != nil {
		return Config{}, err
	}
	if cfg.AI.MaxRetries, err = envInt("AI_MAX_RETRIES", cfg.AI.MaxRetries); err != nil {
		return Config{}, err
	}
	if cfg.AI.MaxCompletions, err = envInt("AI_MAX_COMPLETIONS", cfg.AI.MaxCompletions); err != nil {
		return Config{}, err
	}
	if cfg.AI.JSONRepair, err = envBool("AI_JSON_REPAIR", cfg.AI.JSONRepair); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.EURouting, err = envBool("OPENROUTER_EU_ROUTING", cfg.OpenRouter.EURouting); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.PrivacyStrict, err = envBoolAlias("OPENROUTER_PRIVACY_STRICT", "PRIVACY_STRICT", cfg.OpenRouter.PrivacyStrict); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.ZDR, err = envBool("OPENROUTER_ZDR", cfg.OpenRouter.ZDR); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.AllowFallbacks, err = envBool("OPENROUTER_ALLOW_FALLBACKS", cfg.OpenRouter.AllowFallbacks); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.RequireParameters, err = envBool("OPENROUTER_REQUIRE_PARAMETERS", cfg.OpenRouter.RequireParameters); err != nil {
		return Config{}, err
	}
	if cfg.OpenRouter.RouterMetadata, err = envBool("OPENROUTER_ROUTER_METADATA", cfg.OpenRouter.RouterMetadata); err != nil {
		return Config{}, err
	}
	if cfg.Review.DeepVerification, err = envBool("DEEP_REVIEW_VERIFICATION", cfg.Review.DeepVerification); err != nil {
		return Config{}, err
	}
	if cfg.Review.ReviewDrafts, err = envBool("REVIEW_DRAFTS", cfg.Review.ReviewDrafts); err != nil {
		return Config{}, err
	}
	// Back-compat alias used by older deployments.
	if cfg.Review.ReviewDrafts, err = envBool("AUTO_REVIEW_DRAFTS", cfg.Review.ReviewDrafts); err != nil {
		return Config{}, err
	}
	if cfg.Review.AutoIncremental, err = envBool("AUTO_INCREMENTAL_REVIEW", cfg.Review.AutoIncremental); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxDiffFiles, err = envInt("MAX_DIFF_FILES", cfg.Review.MaxDiffFiles); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxDiffChars, err = envInt("MAX_DIFF_CHARS", cfg.Review.MaxDiffChars); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxFileContentChars, err = envInt("MAX_FILE_CONTENT_CHARS", cfg.Review.MaxFileContentChars); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxContextFiles, err = envInt("MAX_CONTEXT_FILES", cfg.Review.MaxContextFiles); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxTreeEntries, err = envInt("MAX_TREE_ENTRIES", cfg.Review.MaxTreeEntries); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxComments, err = envInt("MAX_INLINE_COMMENTS", cfg.Review.MaxComments); err != nil {
		return Config{}, err
	}
	if cfg.Review.MinimumConfidence, err = envFloat("MINIMUM_CONFIDENCE", cfg.Review.MinimumConfidence); err != nil {
		return Config{}, err
	}
	if cfg.Review.PostProgress, err = envBool("POST_PROGRESS_COMMENT", cfg.Review.PostProgress); err != nil {
		return Config{}, err
	}
	if cfg.Review.PostSummary, err = envBool("POST_WALKTHROUGH", cfg.Review.PostSummary); err != nil {
		return Config{}, err
	}
	if cfg.Review.SummaryInDescription, err = envBool("SUMMARY_IN_DESCRIPTION", cfg.Review.SummaryInDescription); err != nil {
		return Config{}, err
	}
	if cfg.Review.PostInline, err = envBool("POST_INLINE_COMMENTS", cfg.Review.PostInline); err != nil {
		return Config{}, err
	}
	if cfg.Review.CommitStatus, err = envBool("GITLAB_COMMIT_STATUS_ENABLED", cfg.Review.CommitStatus); err != nil {
		return Config{}, err
	}
	if cfg.Review.BlockOnFindings, err = envBool("BLOCK_ON_FINDINGS", cfg.Review.BlockOnFindings); err != nil {
		return Config{}, err
	}
	if cfg.Review.IncludeLinkedIssues, err = envBool("INCLUDE_LINKED_ISSUES", cfg.Review.IncludeLinkedIssues); err != nil {
		return Config{}, err
	}
	if cfg.Review.IncludeRepositoryTree, err = envBool("INCLUDE_REPOSITORY_TREE", cfg.Review.IncludeRepositoryTree); err != nil {
		return Config{}, err
	}
	if cfg.Review.IncludeChangedFileContent, err = envBool("INCLUDE_CHANGED_FILE_CONTENT", cfg.Review.IncludeChangedFileContent); err != nil {
		return Config{}, err
	}
	if cfg.Review.IncludeRepositoryGuides, err = envBool("INCLUDE_REPOSITORY_GUIDELINES", cfg.Review.IncludeRepositoryGuides); err != nil {
		return Config{}, err
	}
	if cfg.Review.IncludeRelatedFiles, err = envBool("INCLUDE_RELATED_FILES", cfg.Review.IncludeRelatedFiles); err != nil {
		return Config{}, err
	}
	if cfg.Review.MaxRelatedContextFiles, err = envInt("MAX_RELATED_CONTEXT_FILES", cfg.Review.MaxRelatedContextFiles); err != nil {
		return Config{}, err
	}
	if cfg.Review.ApproveOnClean, err = envBool("GITLAB_APPROVE_ON_CLEAN", cfg.Review.ApproveOnClean); err != nil {
		return Config{}, err
	}
	if cfg.Review.DependencyBumps, err = envBool("REVIEW_DEPENDENCY_BUMPS", cfg.Review.DependencyBumps); err != nil {
		return Config{}, err
	}
	if cfg.Review.ApproveDependencyBumps, err = envBool("GITLAB_APPROVE_DEPENDENCY_BUMPS", cfg.Review.ApproveDependencyBumps); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("REVIEW_DEPENDENCY_BUMP_MODE")); raw != "" {
		mode, parseErr := ParseDependencyBumpMode(raw)
		if parseErr != nil {
			return Config{}, fmt.Errorf("REVIEW_DEPENDENCY_BUMP_MODE: %w", parseErr)
		}
		cfg.Review.DependencyBumpMode = mode
	}
	cfg.Review.BumpPushURL = strings.TrimSpace(os.Getenv("DEPENDENCY_BUMP_PUSH_URL"))
	cfg.Review.BumpPushSecret = strings.TrimSpace(os.Getenv("DEPENDENCY_BUMP_PUSH_SECRET"))
	if cfg.Review.RequestReview, err = envBool("GITLAB_REQUEST_REVIEW", cfg.Review.RequestReview); err != nil {
		return Config{}, err
	}
	if cfg.Review.TodoMax, err = envInt("TODO_MAX", cfg.Review.TodoMax); err != nil {
		return Config{}, err
	}
	if cfg.Review.SkipAck, err = envBool("SKIP_MR_ACK", cfg.Review.SkipAck); err != nil {
		return Config{}, err
	}
	if cfg.Target.ProjectID, err = envInt64First([]string{"PROJECT_ID", "CI_PROJECT_ID"}, 0); err != nil {
		return Config{}, err
	}
	if cfg.Target.MergeRequest, err = envInt64First([]string{"MERGE_REQUEST_IID", "CI_MERGE_REQUEST_IID"}, 0); err != nil {
		return Config{}, err
	}
	if cfg.Target.Full, err = envBool("REVIEW_FULL", false); err != nil {
		return Config{}, err
	}
	if cfg.Target.Force, err = envBool("REVIEW_FORCE", false); err != nil {
		return Config{}, err
	}
	if cfg.Target.SummaryOnly, err = envBool("REVIEW_SUMMARY_ONLY", false); err != nil {
		return Config{}, err
	}
	if cfg.Target.DryRun, err = envBool("REVIEW_DRY_RUN", false); err != nil {
		return Config{}, err
	}

	if raw := strings.TrimSpace(os.Getenv("AI_EXTRA_HEADERS_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.AI.ExtraHeaders); err != nil {
			return Config{}, fmt.Errorf("AI_EXTRA_HEADERS_JSON: %w", err)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("PATH_INSTRUCTIONS_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Review.PathInstructions); err != nil {
			return Config{}, fmt.Errorf("PATH_INSTRUCTIONS_JSON: %w", err)
		}
	}
	providerNames := envCSV("AI_PROVIDER", "openai")
	providerNames = append(providerNames, envCSV("AI_ALLOWED_PROVIDERS", "")...)
	if len(providerNames) == 0 {
		return Config{}, errors.New("AI_PROVIDER is required")
	}
	cfg.AI.Provider = strings.ToLower(providerNames[0])
	cfg.enabledProviders, err = parseEnabledProviders(providerNames)
	if err != nil {
		return Config{}, err
	}
	cfg.providerKeys, cfg.customProviders, cfg.keysPrivacy, cfg.providerModels, err = loadProviderKeys(cfg.enabledProviders)
	if err != nil {
		return Config{}, err
	}

	applyProviderDefaults(&cfg)
	cfg.defaultProvider = cfg.AI.Provider
	cfg.defaultAllowlistID = allowlistID(cfg.AI.Provider, cfg.OpenRouter.EURouting)
	if isCustomProviderID(cfg.AI.Provider) {
		cfg.defaultAllowlistID = cfg.AI.Provider
	}
	cfg.applyKeysPrivacy(cfg.defaultAllowlistID)
	if !cfg.modelsFromEnv {
		cfg.applyKeysModels(cfg.defaultAllowlistID)
	}
	applyMentionAliases(&cfg)
	key, err := cfg.resolveAPIKey(cfg.AI.Provider)
	if err != nil {
		return Config{}, err
	}
	cfg.AI.APIKey = key
	cfg.defaultModels = append([]string{}, cfg.AI.Models...)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyTargetOverrides merges CLI flags into the loaded configuration.
func (c *Config) ApplyTargetOverrides(projectID, mergeRequest int64, mode string, full, force, summaryOnly, dryRun bool, setFull, setForce, setSummary, setDryRun bool) {
	if projectID > 0 {
		c.Target.ProjectID = projectID
	}
	if mergeRequest > 0 {
		c.Target.MergeRequest = mergeRequest
	}
	if mode != "" {
		c.Target.Mode = strings.ToLower(mode)
	}
	if setFull {
		c.Target.Full = full
	}
	if setForce {
		c.Target.Force = force
	}
	if setSummary {
		c.Target.SummaryOnly = summaryOnly
	}
	if setDryRun {
		c.Target.DryRun = dryRun
	}
}

func applyProviderDefaults(cfg *Config) {
	id, err := requestedAllowlistID(cfg.AI.Provider)
	if err == nil {
		if spec, ok := cfg.customProviders[id]; ok {
			cfg.AI.Provider = id
			cfg.AI.BaseURL = spec.BaseURL
			if spec.AuthMode != "" {
				cfg.AI.AuthMode = spec.AuthMode
			} else if cfg.AI.AuthMode == "" {
				cfg.AI.AuthMode = "bearer"
			}
			return
		}
	}
	provider, forceEU := normalizeProvider(cfg.AI.Provider)
	cfg.AI.Provider = provider
	if forceEU != nil {
		cfg.OpenRouter.EURouting = *forceEU
	}
	switch cfg.AI.Provider {
	case "openrouter":
		if cfg.AI.BaseURL == "" {
			if cfg.OpenRouter.EURouting {
				cfg.AI.BaseURL = "https://eu.openrouter.ai/api/v1"
			} else {
				cfg.AI.BaseURL = "https://openrouter.ai/api/v1"
			}
		}
		if cfg.AI.AuthMode == "" {
			cfg.AI.AuthMode = "bearer"
		}
		applyOpenRouterPrivacy(cfg)
	case "anthropic":
		if cfg.AI.BaseURL == "" {
			cfg.AI.BaseURL = "https://api.anthropic.com"
		}
		if cfg.AI.AuthMode == "" {
			cfg.AI.AuthMode = "x-api-key"
		}
	case "xai", "grok":
		if cfg.AI.BaseURL == "" {
			cfg.AI.BaseURL = "https://api.x.ai/v1"
		}
		if cfg.AI.AuthMode == "" {
			cfg.AI.AuthMode = "bearer"
		}
		if len(cfg.AI.Models) == 0 {
			cfg.AI.Models = []string{"grok-4.6"}
		}
	default:
		if cfg.AI.BaseURL == "" {
			cfg.AI.BaseURL = "https://api.openai.com/v1"
		}
		if cfg.AI.AuthMode == "" {
			cfg.AI.AuthMode = "bearer"
		}
	}
}

func normalizeProvider(value string) (string, *bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai", "openai-compatible", "openai-compat", "oai":
		return "openai", nil
	case "anthropic", "anthropic-compatible", "anthropic-compat", "claude":
		return "anthropic", nil
	case "openrouter", "or":
		return "openrouter", nil
	case "openrouter-eu", "openrouter_eu", "or-eu", "or_eu":
		value := true
		return "openrouter", &value
	case "openrouter-global", "openrouter_global", "or-global", "or_global":
		value := false
		return "openrouter", &value
	case "xai", "x-ai", "spacexai":
		// Same Grok API host as grok; xai is metered API-key auth.
		return "xai", nil
	case "grok", "grok-build":
		// Same Grok API host as xai; grok is SuperGrok/X Premium+ subscription oauth.
		return "grok", nil
	case "codex", "chatgpt", "openai-codex":
		return "codex", nil
	default:
		return strings.ToLower(strings.TrimSpace(value)), nil
	}
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return data, nil
}

func applyOpenRouterPrivacy(cfg *Config) {
	if cfg.AI.Provider != "openrouter" {
		return
	}
	if cfg.OpenRouter.PrivacyStrict || cfg.OpenRouter.EURouting {
		cfg.OpenRouter.ZDR = true
		cfg.OpenRouter.DataCollection = "deny"
	}
}

func allowlistID(provider string, eu bool) string {
	if provider == "openrouter" && eu {
		return "openrouter-eu"
	}
	return provider
}

func requestedAllowlistID(value string) (string, error) {
	provider, forceEU := normalizeProvider(value)
	if validAIProvider(provider) {
		eu := false
		if forceEU != nil {
			eu = *forceEU
		}
		return allowlistID(provider, eu), nil
	}
	id := strings.ToLower(strings.TrimSpace(value))
	if !validCustomProviderName(id) {
		return "", fmt.Errorf("unsupported provider %q", value)
	}
	return id, nil
}

func parseEnabledProviders(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("AI_PROVIDER is required")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		id, err := requestedAllowlistID(value)
		if err != nil {
			return nil, fmt.Errorf("AI_PROVIDER contains unsupported provider %q", value)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func validAIProvider(value string) bool {
	switch value {
	case "openai", "anthropic", "openrouter", "xai", "grok", "codex":
		return true
	default:
		return false
	}
}

func (c *Config) resolveAPIKey(provider string) (string, error) {
	normalized, forceEU := normalizeProvider(provider)
	eu := c.OpenRouter.EURouting
	if forceEU != nil {
		eu = *forceEU
	}
	id := allowlistID(normalized, eu)
	if !validAIProvider(normalized) {
		if customID, err := requestedAllowlistID(provider); err == nil {
			id = customID
		}
	}
	if key := strings.TrimSpace(c.providerKeys[id]); key != "" {
		return key, nil
	}
	if key := strings.TrimSpace(c.providerKeys[normalized]); key != "" {
		return key, nil
	}
	switch normalized {
	case "grok", "codex":
		// Subscription names use device-login oauth, not AI_API_KEY (that is the xai/openai API path).
		token, err := auth.Resolve(context.Background(), normalized)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(token), nil
	default:
		if isCustomProviderID(id) {
			return "", nil
		}
		return strings.TrimSpace(os.Getenv("AI_API_KEY")), nil
	}
}

// SelectProvider switches to a provider listed in AI_PROVIDER.
func (c *Config) SelectProvider(provider string) error {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return errors.New("provider must not be empty")
	}
	id, err := requestedAllowlistID(provider)
	if err != nil {
		return err
	}
	found := false
	for _, item := range c.enabledProviders {
		if item == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %q is not in AI_PROVIDER (enabled: %s)", id, strings.Join(c.enabledProviders, ", "))
	}
	if spec, ok := c.customProviders[id]; ok {
		if !validHTTPURL(spec.BaseURL) {
			return fmt.Errorf("custom provider %q requires base_url in AI_KEYS_FILE", id)
		}
		c.AI.Provider = id
		c.AI.BaseURL = spec.BaseURL
		c.AI.AuthMode = spec.AuthMode
		if c.AI.AuthMode == "" {
			c.AI.AuthMode = "bearer"
		}
		key, err := c.resolveAPIKey(id)
		if err != nil {
			return err
		}
		c.AI.APIKey = key
		if c.AI.AuthMode != "none" && strings.TrimSpace(c.AI.APIKey) == "" {
			return fmt.Errorf("no credentials configured for provider %q", id)
		}
		c.AI.Models = nil
		c.applyModelsForProvider(id)
		return nil
	}
	normalized, forceEU := normalizeProvider(provider)
	if !validAIProvider(normalized) {
		return fmt.Errorf("AI_PROVIDER must be openai, anthropic, openrouter, xai, grok, or codex, or a custom name listed in AI_PROVIDER, got %q", provider)
	}
	c.AI.Provider = normalized
	if forceEU != nil {
		c.OpenRouter.EURouting = *forceEU
	} else if normalized == "openrouter" && !c.euRoutingFromEnv {
		c.OpenRouter.EURouting = false
	}
	if !c.baseURLFromEnv {
		c.AI.BaseURL = ""
	}
	if !c.authModeFromEnv {
		c.AI.AuthMode = ""
	}
	c.AI.Models = nil
	applyProviderDefaults(c)
	c.applyKeysPrivacy(id)
	c.applyModelsForProvider(id)
	if c.AI.Provider == "openrouter" && c.OpenRouter.EURouting {
		u, err := url.Parse(c.AI.BaseURL)
		if err != nil || !strings.EqualFold(u.Hostname(), "eu.openrouter.ai") {
			return errors.New("openrouter-eu requires AI_BASE_URL to use eu.openrouter.ai")
		}
	}
	key, err := c.resolveAPIKey(normalized)
	if err != nil {
		return err
	}
	c.AI.APIKey = key
	if c.AI.AuthMode != "none" && strings.TrimSpace(c.AI.APIKey) == "" {
		return fmt.Errorf("no credentials configured for provider %q", normalized)
	}
	return nil
}

func (c *Config) applyKeysModels(id string) {
	if models := c.providerModels[id]; len(models) > 0 {
		c.AI.Models = append([]string{}, models...)
	}
}

func (c *Config) applyModelsForProvider(id string) {
	c.applyKeysModels(id)
	if len(c.AI.Models) > 0 {
		return
	}
	if id == c.defaultAllowlistID && len(c.defaultModels) > 0 {
		c.AI.Models = append([]string{}, c.defaultModels...)
	}
}

func (c *Config) applyKeysPrivacy(id string) {
	overlay, ok := c.keysPrivacy[id]
	if !ok {
		return
	}
	if overlay.zdr != nil {
		c.OpenRouter.ZDR = *overlay.zdr
		c.OpenRouter.IncludeZDR = true
	} else {
		c.OpenRouter.IncludeZDR = false
	}
	if overlay.dataCollection != nil {
		c.OpenRouter.DataCollection = *overlay.dataCollection
		c.OpenRouter.IncludeDataCollection = true
	} else {
		c.OpenRouter.IncludeDataCollection = false
	}
}

func applyMentionAliases(cfg *Config) {
	aliases := append([]string{cfg.Review.Mention, cfg.GitLab.BotUsername}, cfg.Review.MentionAliases...)
	seen := map[string]struct{}{}
	cfg.Review.MentionAliases = cfg.Review.MentionAliases[:0]
	for _, alias := range aliases {
		alias = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(alias, "@")))
		if alias == "" {
			continue
		}
		if _, exists := seen[alias]; exists {
			continue
		}
		seen[alias] = struct{}{}
		cfg.Review.MentionAliases = append(cfg.Review.MentionAliases, alias)
	}
	if len(cfg.Review.MentionAliases) > 0 {
		cfg.Review.Mention = cfg.Review.MentionAliases[0]
	}
}

func (c Config) Validate() error {
	var errs []error
	for name, duration := range map[string]time.Duration{
		"JOB_TIMEOUT":    c.JobTimeout,
		"GITLAB_TIMEOUT": c.GitLab.Timeout,
		"AI_TIMEOUT":     c.AI.Timeout,
	} {
		if duration <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.AI.MaxTokens <= 0 {
		errs = append(errs, errors.New("AI_MAX_TOKENS must be positive"))
	}
	if c.AI.MaxInputChars <= 0 {
		errs = append(errs, errors.New("AI_MAX_INPUT_CHARS must be positive"))
	}
	if c.AI.Temperature < 0 {
		errs = append(errs, errors.New("AI_TEMPERATURE must not be negative"))
	}
	if c.AI.MaxRetries < 0 {
		errs = append(errs, errors.New("AI_MAX_RETRIES must not be negative"))
	}
	if c.AI.MaxCompletions < 1 {
		errs = append(errs, errors.New("AI_MAX_COMPLETIONS must be at least 1"))
	}
	if c.GitLab.MaxRetries < 0 {
		errs = append(errs, errors.New("GITLAB_MAX_RETRIES must not be negative"))
	}
	if c.Review.TodoMax < 1 {
		errs = append(errs, errors.New("TODO_MAX must be at least 1"))
	}
	if c.GitLab.APIURL == "" || c.GitLab.Token == "" {
		errs = append(errs, errors.New("GITLAB_API_URL (or CI_API_V4_URL) and GITLAB_TOKEN (or CI_JOB_TOKEN) are required"))
	}
	if c.GitLab.APIURL != "" && !validHTTPURL(c.GitLab.APIURL) {
		errs = append(errs, errors.New("GITLAB_API_URL must be an absolute http(s) URL"))
	}
	if !validAIProvider(c.AI.Provider) && !isCustomProviderID(c.AI.Provider) {
		errs = append(errs, fmt.Errorf("AI_PROVIDER must be openai, anthropic, openrouter, xai, grok, or codex, or a custom name listed in AI_PROVIDER, got %q", c.AI.Provider))
	}
	if !validHTTPURL(c.AI.BaseURL) {
		errs = append(errs, errors.New("AI_BASE_URL must be an absolute http(s) URL"))
	}
	if pathURL, err := url.Parse(c.AI.ChatCompletionsPath); err == nil && pathURL.IsAbs() {
		errs = append(errs, errors.New("OPENAI_CHAT_COMPLETIONS_PATH must be a relative path"))
	}
	if pathURL, err := url.Parse(c.AI.AnthropicMessagesPath); err == nil && pathURL.IsAbs() {
		errs = append(errs, errors.New("ANTHROPIC_MESSAGES_PATH must be a relative path"))
	}
	if len(c.AI.Models) == 0 {
		errs = append(errs, errors.New("AI_MODEL or AI_MODELS is required"))
	}
	if c.AI.AuthMode != "none" && c.AI.APIKey == "" {
		errs = append(errs, errors.New("AI credentials are required unless AI_AUTH_MODE=none (AI_KEYS_FILE, AI_KEYS_JSON, AI_API_KEY, or `codereview login` for grok/codex)"))
	}
	switch c.AI.AuthMode {
	case "bearer", "x-api-key", "api-key", "none":
	default:
		errs = append(errs, fmt.Errorf("unsupported AI_AUTH_MODE %q", c.AI.AuthMode))
	}
	if c.AI.JSONMode != "prompt" && c.AI.JSONMode != "json_object" {
		errs = append(errs, errors.New("AI_JSON_MODE must be prompt or json_object"))
	}
	if c.Review.MinimumConfidence < 0 || c.Review.MinimumConfidence > 1 {
		errs = append(errs, errors.New("MINIMUM_CONFIDENCE must be between 0 and 1"))
	}
	for name, value := range map[string]int{
		"MAX_DIFF_FILES":            c.Review.MaxDiffFiles,
		"MAX_DIFF_CHARS":            c.Review.MaxDiffChars,
		"MAX_FILE_CONTENT_CHARS":    c.Review.MaxFileContentChars,
		"MAX_CONTEXT_FILES":         c.Review.MaxContextFiles,
		"MAX_TREE_ENTRIES":          c.Review.MaxTreeEntries,
		"MAX_INLINE_COMMENTS":       c.Review.MaxComments,
		"MAX_RELATED_CONTEXT_FILES": c.Review.MaxRelatedContextFiles,
	} {
		if value < 0 {
			errs = append(errs, fmt.Errorf("%s must not be negative", name))
		}
	}
	if c.Review.Mention == "" {
		errs = append(errs, errors.New("BOT_MENTION must not be empty"))
	}
	if !validReviewMode(c.Review.DefaultMode) {
		errs = append(errs, errors.New("DEFAULT_REVIEW_MODE must be quick, standard, deep, or security"))
	}
	if c.Target.Mode != "" && !validReviewMode(c.Target.Mode) {
		errs = append(errs, errors.New("REVIEW_MODE must be quick, standard, deep, or security"))
	}
	if c.Review.CommentStyle != "compact" && c.Review.CommentStyle != "detailed" {
		errs = append(errs, errors.New("COMMENT_STYLE must be compact or detailed"))
	}
	for _, mode := range c.Review.BlockingReviewModes {
		if !validReviewMode(mode) {
			errs = append(errs, fmt.Errorf("BLOCKING_REVIEW_MODES contains unsupported mode %q", mode))
		}
	}
	for _, mode := range c.Review.StatusReviewModes {
		if !validReviewMode(mode) {
			errs = append(errs, fmt.Errorf("STATUS_REVIEW_MODES contains unsupported mode %q", mode))
		}
	}
	for index, item := range c.Review.PathInstructions {
		if strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Instructions) == "" {
			errs = append(errs, fmt.Errorf("PATH_INSTRUCTIONS_JSON entry %d requires non-empty path and instructions", index))
		}
	}
	if err := c.validateDependencyBumpSettings(); err != nil {
		errs = append(errs, err)
	}
	if c.AI.Provider == "openrouter" {
		if c.OpenRouter.IncludeDataCollection && c.OpenRouter.DataCollection != "allow" && c.OpenRouter.DataCollection != "deny" {
			errs = append(errs, errors.New("OPENROUTER_DATA_COLLECTION must be allow or deny"))
		}
		if c.OpenRouter.EURouting {
			u, err := url.Parse(c.AI.BaseURL)
			if err != nil || !strings.EqualFold(u.Hostname(), "eu.openrouter.ai") {
				errs = append(errs, errors.New("OPENROUTER_EU_ROUTING requires AI_BASE_URL to use eu.openrouter.ai"))
			}
		}
	}
	return errors.Join(errs...)
}

// ValidateTarget checks that a merge request identity was provided for this run.
func (c Config) ValidateTarget() error {
	if c.Target.ProjectID <= 0 {
		return errors.New("PROJECT_ID / CI_PROJECT_ID or --project is required")
	}
	if c.Target.MergeRequest <= 0 {
		return errors.New("MERGE_REQUEST_IID / CI_MERGE_REQUEST_IID or --mr is required")
	}
	return nil
}

func validReviewMode(value string) bool {
	switch value {
	case "quick", "standard", "deep", "security":
		return true
	default:
		return false
	}
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func envIsSet(key string) bool {
	raw, ok := os.LookupEnv(key)
	return ok && strings.TrimSpace(raw) != ""
}

func envBool(key string, fallback bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func envBoolAlias(primary, alias string, fallback bool) (bool, error) {
	for _, key := range []string{primary, alias} {
		raw, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, fmt.Errorf("%s: %w", key, err)
		}
		return value, nil
	}
	return fallback, nil
}

func envInt(key string, fallback int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func envInt64(key string, fallback int64) (int64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func envInt64First(keys []string, fallback int64) (int64, error) {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", key, err)
		}
		return value, nil
	}
	return fallback, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func envCSV(key, fallback string) []string {
	raw := env(key, fallback)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func lowerValues(values []string) []string {
	for index := range values {
		values[index] = strings.ToLower(values[index])
	}
	return values
}
