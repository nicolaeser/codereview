package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxRepoFileBytes = 64 * 1024

// RepoFile is reviewed-repository policy from `.codereview.yml` at the reviewed SHA.
// It is not an operator-secret surface and cannot set API keys, base URLs, or privacy.
type RepoFile struct {
	Review RepoReviewFile `yaml:"review"`
}

type RepoReviewFile struct {
	Mode                   *string           `yaml:"mode"`
	Drafts                 *bool             `yaml:"drafts"`
	DependencyBumps        *bool             `yaml:"dependency_bumps"`
	ApproveDependencyBumps *bool             `yaml:"approve_dependency_bumps"`
	DependencyBumpMode     *string           `yaml:"dependency_bump_mode"`
	BumpPushURL            *string           `yaml:"dependency_bump_push_url"`
	BumpPushSecret         *string           `yaml:"dependency_bump_push_secret"`
	IgnorePaths            []string          `yaml:"ignore_paths"`
	Mention                *string           `yaml:"mention"`
	MentionAliases         []string          `yaml:"mention_aliases"`
	PathInstructions       []PathInstruction `yaml:"path_instructions"`
	Provider               *string           `yaml:"provider"`
	Model                  *string           `yaml:"model"`
	Models                 []string          `yaml:"models"`
	SkipTokens             []string          `yaml:"skip_tokens"`
	SkipAck                *bool             `yaml:"skip_ack"`
}

// ParseRepoFile decodes optional `.codereview.yml` / `.codereview.yaml` content.
// Invalid YAML or values that cannot unmarshal into the typed policy fail closed.
// Unknown extra keys are ignored so forbidden routing/secret fields cannot be set.
func ParseRepoFile(raw []byte) (RepoFile, error) {
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	if len(raw) > maxRepoFileBytes {
		return RepoFile{}, fmt.Errorf("review policy file exceeds %d bytes", maxRepoFileBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return RepoFile{}, nil
	}
	var file RepoFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return RepoFile{}, fmt.Errorf("parse review policy: %w", err)
	}
	return file, nil
}

// ApplyRepoFile overlays repository policy onto compiled defaults.
// Process environment / already-applied CLI values win for review behavior.
// review.provider must be listed in AI_PROVIDER and have a usable keys-file entry; review.model/models override instance AI_MODEL.
// ignore_paths and path_instructions merge (repository first, then operator-configured entries).
func (c *Config) ApplyRepoFile(file RepoFile) error {
	review := file.Review
	if review.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*review.Mode))
		if !validReviewMode(mode) {
			return fmt.Errorf(".codereview.yml review.mode must be quick, standard, deep, or security")
		}
		if !RepoEnvSet("DEFAULT_REVIEW_MODE") {
			c.Review.DefaultMode = mode
		}
	}
	if review.Drafts != nil && !RepoEnvSet("REVIEW_DRAFTS", "AUTO_REVIEW_DRAFTS") {
		c.Review.ReviewDrafts = *review.Drafts
	}
	if review.DependencyBumps != nil && !RepoEnvSet("REVIEW_DEPENDENCY_BUMPS") {
		c.Review.DependencyBumps = *review.DependencyBumps
	}
	if review.ApproveDependencyBumps != nil && !RepoEnvSet("GITLAB_APPROVE_DEPENDENCY_BUMPS") {
		c.Review.ApproveDependencyBumps = *review.ApproveDependencyBumps
	}
	if review.DependencyBumpMode != nil && !RepoEnvSet("REVIEW_DEPENDENCY_BUMP_MODE") {
		mode, err := ParseDependencyBumpMode(*review.DependencyBumpMode)
		if err != nil {
			return fmt.Errorf(".codereview.yml review.dependency_bump_mode: %w", err)
		}
		c.Review.DependencyBumpMode = mode
	}
	// yaml cannot set PUSH URL/secret even when those keys are present.
	_ = review.BumpPushURL
	_ = review.BumpPushSecret
	if review.Mention != nil {
		mention := strings.TrimPrefix(strings.TrimSpace(*review.Mention), "@")
		if mention == "" {
			return fmt.Errorf(".codereview.yml review.mention must not be empty")
		}
		if !RepoEnvSet("BOT_MENTION") {
			c.Review.Mention = mention
		}
	}
	if review.MentionAliases != nil && !RepoEnvSet("BOT_MENTION_ALIASES") {
		c.Review.MentionAliases = append([]string{}, review.MentionAliases...)
	}
	if review.IgnorePaths != nil {
		c.Review.IgnorePaths = mergeStringLists(review.IgnorePaths, c.Review.IgnorePaths)
	}
	if review.PathInstructions != nil {
		for index, item := range review.PathInstructions {
			if strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Instructions) == "" {
				return fmt.Errorf(".codereview.yml review.path_instructions[%d] requires non-empty path and instructions", index)
			}
		}
		merged := make([]PathInstruction, 0, len(review.PathInstructions)+len(c.Review.PathInstructions))
		merged = append(merged, review.PathInstructions...)
		merged = append(merged, c.Review.PathInstructions...)
		c.Review.PathInstructions = merged
	}
	if review.Provider != nil {
		if err := c.SelectProvider(*review.Provider); err != nil {
			return fmt.Errorf(".codereview.yml review.provider: %w", err)
		}
	}
	models, err := repoFileModels(review)
	if err != nil {
		return err
	}
	if models != nil {
		c.AI.Models = models
	}
	if review.SkipTokens != nil {
		c.Review.SkipTokens = mergeStringLists(review.SkipTokens, c.Review.SkipTokens)
	}
	if review.SkipAck != nil && !RepoEnvSet("SKIP_MR_ACK") {
		c.Review.SkipAck = *review.SkipAck
	}
	applyMentionAliases(c)
	if c.Review.Mention == "" {
		return fmt.Errorf(".codereview.yml review.mention must not be empty")
	}
	if !validReviewMode(c.Review.DefaultMode) {
		return fmt.Errorf("DEFAULT_REVIEW_MODE must be quick, standard, deep, or security")
	}
	if c.Target.Mode != "" && !validReviewMode(c.Target.Mode) {
		return fmt.Errorf("REVIEW_MODE must be quick, standard, deep, or security")
	}
	return nil
}

func repoFileModels(review RepoReviewFile) ([]string, error) {
	if len(review.Models) > 0 {
		out := make([]string, 0, len(review.Models))
		for index, model := range review.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				return nil, fmt.Errorf(".codereview.yml review.models[%d] must not be empty", index)
			}
			out = append(out, model)
		}
		return out, nil
	}
	if review.Model == nil {
		return nil, nil
	}
	model := strings.TrimSpace(*review.Model)
	if model == "" {
		return nil, fmt.Errorf(".codereview.yml review.model must not be empty")
	}
	return []string{model}, nil
}

// RepoEnvSet reports whether any named process environment variable is set
// to a non-empty value. Operator env wins over `.codereview.yml`.
func RepoEnvSet(keys ...string) bool {
	for _, key := range keys {
		if raw, ok := os.LookupEnv(key); ok && strings.TrimSpace(raw) != "" {
			return true
		}
	}
	return false
}

func mergeStringLists(head, tail []string) []string {
	seen := make(map[string]struct{}, len(head)+len(tail))
	out := make([]string, 0, len(head)+len(tail))
	for _, list := range [][]string{head, tail} {
		for _, item := range list {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}
