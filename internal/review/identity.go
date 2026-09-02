package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	lineMarkerPrefix = "<!-- codereview-line:"
	lineMarkerSuffix = " -->"
)

func normalizeAddedLine(raw string) string {
	text := strings.TrimPrefix(raw, "+")
	return strings.TrimRight(text, " \t\r")
}

func lineContentID(raw string) string {
	text := normalizeAddedLine(raw)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:findingIDHexChars]
}

func lineContentMarker(raw string) string {
	id := lineContentID(raw)
	if id == "" {
		return ""
	}
	return lineMarkerPrefix + id + lineMarkerSuffix
}

func parseLineContentID(body string) (string, bool) {
	start := strings.Index(body, lineMarkerPrefix)
	if start < 0 {
		return "", false
	}
	rest := body[start+len(lineMarkerPrefix):]
	end := strings.Index(rest, lineMarkerSuffix)
	if end < 0 {
		return "", false
	}
	id := strings.ToLower(rest[:end])
	if id == "" || !isHex(id) {
		return "", false
	}
	return id, true
}

func lineContentIDFromDiffs(diffs []PreparedDiff, path string, line int) string {
	prepared, ok := findPreparedDiff(diffs, path)
	if !ok || prepared.AddedLines == nil {
		return ""
	}
	return lineContentID(prepared.AddedLines[line])
}

func contentKey(path, contentID string) string {
	if path == "" || contentID == "" {
		return ""
	}
	return path + "\x00" + contentID
}

func findingContentKey(finding Finding) string {
	return contentKey(finding.Path, lineContentID(finding.LineText))
}

func contentIDsInDiffs(diffs []PreparedDiff) map[string]struct{} {
	out := map[string]struct{}{}
	for _, prepared := range diffs {
		path := prepared.Diff.NewPath
		if prepared.Diff.DeletedFile {
			path = prepared.Diff.OldPath
		}
		for _, text := range prepared.AddedLines {
			if id := lineContentID(text); id != "" {
				out[contentKey(path, id)] = struct{}{}
			}
		}
	}
	return out
}

func hunkContainsDiscussion(hunk []PreparedDiff, discussion ownedDiscussion) bool {
	prepared, ok := findPreparedDiff(hunk, discussion.Path)
	if !ok {
		return false
	}
	if discussion.HasLocation {
		if _, changed := prepared.ChangedLines[discussion.Line]; changed {
			return true
		}
	}
	if discussion.LineContentID == "" {
		return false
	}
	for _, text := range prepared.AddedLines {
		if lineContentID(text) == discussion.LineContentID {
			return true
		}
	}
	return false
}

func fullDiffStillHasDiscussion(full []PreparedDiff, discussion ownedDiscussion) bool {
	if key := contentKey(discussion.Path, discussion.LineContentID); key != "" {
		_, ok := contentIDsInDiffs(full)[key]
		return ok
	}
	if discussion.HasLocation {
		prepared, ok := findPreparedDiff(full, discussion.Path)
		if !ok {
			return false
		}
		_, changed := prepared.ChangedLines[discussion.Line]
		return changed
	}
	return false
}

func leftoverOpenCount(owned []ownedDiscussion, skipped skippedThreads, current []Finding, full []PreparedDiff) int {
	n := 0
	for _, discussion := range owned {
		if !discussion.Unresolved {
			continue
		}
		if skipped.hasDiscussion(discussion) {
			continue
		}
		matched := false
		for _, finding := range current {
			if discussionMatchesFinding(discussion, finding, full) {
				matched = true
				break
			}
			if discussionNearFindingLine(discussion, finding) && titlesLikelySame(discussion.Title, finding.Title) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if fullDiffStillHasDiscussion(full, discussion) {
			n++
		}
	}
	return n
}

const neighborLineWindow = 1

func lineDistance(a, b int) int {
	if a < b {
		return b - a
	}
	return a - b
}

func discussionNearFindingLine(discussion ownedDiscussion, finding Finding) bool {
	if !discussion.HasLocation || discussion.Path == "" || discussion.Path != finding.Path || finding.Line <= 0 {
		return false
	}
	return lineDistance(discussion.Line, finding.Line) <= neighborLineWindow
}

func uniqueNearOwnedDiscussion(owned []ownedDiscussion, finding Finding, used map[string]struct{}) (ownedDiscussion, bool) {
	var near []ownedDiscussion
	for _, discussion := range owned {
		if !discussion.Unresolved {
			continue
		}
		if used != nil {
			if _, seen := used[discussion.ID]; seen {
				continue
			}
		}
		if discussionNearFindingLine(discussion, finding) {
			near = append(near, discussion)
		}
	}
	if len(near) == 1 {
		return near[0], true
	}
	var titled []ownedDiscussion
	for _, discussion := range near {
		if titlesLikelySame(discussion.Title, finding.Title) {
			titled = append(titled, discussion)
		}
	}
	if len(titled) == 1 {
		return titled[0], true
	}
	return ownedDiscussion{}, false
}

func discussionMatchesFinding(discussion ownedDiscussion, finding Finding, diffs []PreparedDiff) bool {
	if discussion.FindingID != "" && discussion.FindingID == FindingID(finding) {
		return true
	}
	if discussion.HasLocation && discussion.Path == finding.Path && discussion.Line == finding.Line {
		return true
	}
	if discussion.LineContentID == "" || discussion.Path == "" {
		return false
	}
	want := contentKey(discussion.Path, discussion.LineContentID)
	for _, key := range contentKeysAroundFinding(finding, diffs) {
		if key == want {
			return true
		}
	}
	return false
}

// contentKeysAroundFinding is path + normalized added-line text at the reported
// line only. Neighbor line numbers are matched separately via unique-near so
// an adjacent different bug is not merged by borrowing that line's text.
func contentKeysAroundFinding(finding Finding, diffs []PreparedDiff) []string {
	seen := map[string]struct{}{}
	var keys []string
	add := func(key string) {
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add(findingContentKey(finding))
	add(contentKey(finding.Path, lineContentIDFromDiffs(diffs, finding.Path, finding.Line)))
	return keys
}
