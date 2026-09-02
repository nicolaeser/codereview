package review

import "strings"

const (
	ignoreFileName    = ".codereviewignore"
	ignoreFileAltName = ".codereview/ignore"
)

// ParseIgnoreFile reads gitignore-style path globs: one pattern per line,
// # comments and blank lines ignored. Leading '/' is stripped so patterns
// match repository-relative paths used in diffs.
func ParseIgnoreFile(raw string) []string {
	var patterns []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		patterns = append(patterns, line)
	}
	return patterns
}
