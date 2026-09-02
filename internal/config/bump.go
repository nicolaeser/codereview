package config

import (
	"fmt"
	"strings"
)

const (
	DependencyBumpModeOff      = ""
	DependencyBumpModeNotify   = "notify"
	DependencyBumpModeWait     = "wait"
	DependencyBumpModeValidate = "validate"
)

// ParseDependencyBumpMode normalizes a bump-mode name. Empty, off, none, and
// disabled are the safe default (ordinary review, no auto-approve). Outbound
// bump webhooks are operator DEPENDENCY_BUMP_PUSH_URL, not a mode.
func ParseDependencyBumpMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "off", "none", "disabled":
		return DependencyBumpModeOff, nil
	case DependencyBumpModeNotify:
		return DependencyBumpModeNotify, nil
	case DependencyBumpModeWait:
		return DependencyBumpModeWait, nil
	case DependencyBumpModeValidate:
		return DependencyBumpModeValidate, nil
	case "agent_report", "agent-report", "report", "push":
		return "", fmt.Errorf("dependency bump mode must be notify, wait, or validate; outbound webhook is DEPENDENCY_BUMP_PUSH_URL, not a mode (got %q)", raw)
	default:
		return "", fmt.Errorf("dependency bump mode must be notify, wait, or validate, got %q", raw)
	}
}

func (c Config) validateDependencyBumpSettings() error {
	switch c.Review.DependencyBumpMode {
	case DependencyBumpModeOff, DependencyBumpModeNotify, DependencyBumpModeWait, DependencyBumpModeValidate:
	default:
		return fmt.Errorf("REVIEW_DEPENDENCY_BUMP_MODE must be notify, wait, or validate, got %q", c.Review.DependencyBumpMode)
	}
	if url := strings.TrimSpace(c.Review.BumpPushURL); url != "" && !validHTTPURL(url) {
		return fmt.Errorf("DEPENDENCY_BUMP_PUSH_URL must be an absolute http(s) URL")
	}
	return nil
}
