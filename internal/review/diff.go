package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func PrepareDiffs(diffs []gitlab.Diff, ignorePatterns []string, maxFiles, maxChars int) ([]PreparedDiff, []string) {
	prepared := make([]PreparedDiff, 0, len(diffs))
	var notices []string
	totalChars := 0
	for _, diff := range diffs {
		path := diff.NewPath
		if diff.DeletedFile {
			path = diff.OldPath
		}
		if matchesAny(path, ignorePatterns) || diff.Generated {
			continue
		}
		if diff.TooLarge || diff.Collapsed || diff.Diff == "" {
			notices = append(notices, fmt.Sprintf("`%s` was omitted because GitLab did not return a reviewable diff.", path))
			continue
		}
		if len(prepared) >= maxFiles {
			notices = append(notices, fmt.Sprintf("The review was limited to the first %d files.", maxFiles))
			break
		}
		remaining := maxChars - totalChars
		if remaining <= 0 {
			notices = append(notices, fmt.Sprintf("The diff was truncated after %d characters.", maxChars))
			break
		}
		if len(diff.Diff) > remaining {
			diff.Diff = diff.Diff[:remaining] + "\n[diff truncated by CodeReview]"
			notices = append(notices, fmt.Sprintf("The diff for `%s` was truncated to stay within the configured review budget.", path))
		}
		totalChars += len(diff.Diff)
		changed, added := parseAddedLines(diff.Diff)
		prepared = append(prepared, PreparedDiff{Diff: diff, ChangedLines: changed, AddedLines: added})
	}
	return prepared, notices
}

func changedNewLines(diff string) map[int]struct{} {
	changed, _ := parseAddedLines(diff)
	return changed
}

func parseAddedLines(diff string) (map[int]struct{}, map[int]string) {
	changed := map[int]struct{}{}
	added := map[int]string{}
	newLine := 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			newLine, _ = strconv.Atoi(match[1])
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				changed[newLine] = struct{}{}
				added[newLine] = line[1:]
				newLine++
			}
		case '-':
		case ' ':
			newLine++
		case '\\':
		default:
			newLine++
		}
	}
	return changed, added
}

func matchesAny(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, path) {
			return true
		}
	}
	return false
}

func globMatch(pattern, value string) bool {
	var expression strings.Builder
	expression.WriteByte('^')
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				expression.WriteString(".*")
				i++
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteByte('$')
	matched, err := regexp.MatchString(expression.String(), value)
	return err == nil && matched
}

func findPreparedDiff(diffs []PreparedDiff, path string) (PreparedDiff, bool) {
	for _, diff := range diffs {
		if diff.Diff.NewPath == path || diff.Diff.OldPath == path {
			return diff, true
		}
	}
	return PreparedDiff{}, false
}
