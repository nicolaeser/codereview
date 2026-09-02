package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

const (
	summaryStart        = "<!-- codereview-summary:start -->"
	summaryEnd          = "<!-- codereview-summary:end -->"
	findingMarkerPrefix = "<!-- codereview-finding:"
	findingMarkerSuffix = " -->"
	findingIDHexChars   = 16
	walkthroughMarker   = "<!-- codereview-walkthrough -->"
	walkthroughHeading  = "## CodeReview walkthrough"
)

func MergeReviews(parts []AIReview) AIReview {
	var merged AIReview
	seenWalkthrough := map[string]struct{}{}
	seenFinding := map[string]struct{}{}
	seenIssue := map[int64]struct{}{}
	var summaries []string
	for _, part := range parts {
		if summary := strings.TrimSpace(part.Summary); summary != "" {
			summaries = append(summaries, summary)
		}
		for _, item := range part.Walkthrough {
			key := item.Path + "\x00" + item.Summary
			if _, ok := seenWalkthrough[key]; ok {
				continue
			}
			seenWalkthrough[key] = struct{}{}
			merged.Walkthrough = append(merged.Walkthrough, item)
		}
		for _, finding := range part.Findings {
			key := fmt.Sprintf("%s:%d:%s", finding.Path, finding.Line, strings.ToLower(finding.Title))
			if _, ok := seenFinding[key]; ok {
				continue
			}
			seenFinding[key] = struct{}{}
			merged.Findings = append(merged.Findings, finding)
		}
		for _, assessment := range part.IssueAssessments {
			if _, exists := seenIssue[assessment.IID]; exists {
				continue
			}
			seenIssue[assessment.IID] = struct{}{}
			merged.IssueAssessments = append(merged.IssueAssessments, assessment)
		}
		if merged.AgentReport == "" && strings.TrimSpace(part.AgentReport) != "" {
			merged.AgentReport = strings.TrimSpace(part.AgentReport)
		}
		if merged.BranchName == "" && strings.TrimSpace(part.BranchName) != "" {
			merged.BranchName = strings.TrimSpace(part.BranchName)
		}
		if part.CodeAdaptationRequired != nil {
			required := *part.CodeAdaptationRequired
			if merged.CodeAdaptationRequired == nil || required {
				merged.CodeAdaptationRequired = &required
			}
		}
	}
	merged.Summary = strings.Join(summaries, "\n\n")
	return merged
}

func ValidateFindings(findings []Finding, diffs []PreparedDiff, minimum float64, maximum int) ([]Finding, []Finding) {
	valid := make([]Finding, 0, len(findings))
	rejected := make([]Finding, 0)
	for _, finding := range findings {
		finding.Severity = strings.ToLower(strings.TrimSpace(finding.Severity))
		finding.Category = strings.ToLower(strings.TrimSpace(finding.Category))
		prepared, exists := findPreparedDiff(diffs, finding.Path)
		_, changed := prepared.ChangedLines[finding.Line]
		if !validFindingPath(finding.Path) || !exists || !changed || finding.Confidence < minimum || finding.Title == "" || finding.Body == "" {
			rejected = append(rejected, finding)
			continue
		}
		if !validSeverity(finding.Severity) {
			finding.Severity = "medium"
		}
		if prepared.AddedLines != nil {
			finding.LineText = normalizeAddedLine(prepared.AddedLines[finding.Line])
		}
		finding.Suggestion = safeSuggestion(finding, prepared.AddedLines)
		valid = append(valid, finding)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		left, right := severityRank(valid[i].Severity), severityRank(valid[j].Severity)
		if left != right {
			return left > right
		}
		return valid[i].Confidence > valid[j].Confidence
	})
	if maximum >= 0 && len(valid) > maximum {
		rejected = append(rejected, valid[maximum:]...)
		valid = valid[:maximum]
	}
	collapsed := make([]Finding, 0, len(valid))
	seenLine := map[string]struct{}{}
	for _, finding := range valid {
		key := finding.Path + "\x00" + strconv.Itoa(finding.Line)
		if _, exists := seenLine[key]; exists {
			rejected = append(rejected, finding)
			continue
		}
		seenLine[key] = struct{}{}
		collapsed = append(collapsed, finding)
	}
	return collapsed, rejected
}

func ValidateIssueAssessments(assessments []IssueAssessment, issues []gitlab.Issue) []IssueAssessment {
	allowed := map[int64]struct{}{}
	for _, issue := range issues {
		allowed[issue.IID] = struct{}{}
	}
	seen := map[int64]struct{}{}
	result := make([]IssueAssessment, 0, len(assessments))
	for _, assessment := range assessments {
		if _, exists := allowed[assessment.IID]; !exists || strings.TrimSpace(assessment.Summary) == "" {
			continue
		}
		if _, exists := seen[assessment.IID]; exists {
			continue
		}
		seen[assessment.IID] = struct{}{}
		assessment.Status = strings.ToLower(strings.TrimSpace(assessment.Status))
		if assessment.Status != "addressed" && assessment.Status != "not_addressed" && assessment.Status != "unclear" {
			assessment.Status = "unclear"
		}
		result = append(result, assessment)
	}
	return result
}

func FormatFinding(f Finding, style string) string {
	icon := map[string]string{"critical": "🚨", "high": "⚠️", "medium": "🟠", "low": "🟡"}[f.Severity]
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s **%s** · `%s`", icon, truncateInline(sanitizeModelText(f.Title), 180), f.Severity)
	if style == "detailed" {
		fmt.Fprintf(&builder, " · `%s` · %.0f%% confidence", f.Category, f.Confidence*100)
	}
	bodyLimit := 1200
	if style == "detailed" {
		bodyLimit = 3000
	}
	fmt.Fprintf(&builder, "\n\n%s", truncate(strings.TrimSpace(sanitizeModelText(f.Body)), bodyLimit))
	if suggestion := usableSuggestion(f.Suggestion); suggestion != "" {
		builder.WriteString("\n\n<details><summary>Committable suggestion</summary>\n\n```suggestion\n")
		builder.WriteString(suggestion)
		builder.WriteString("\n```\n</details>")
	}
	builder.WriteString("\n\n<sub>Generated by CodeReview. Verify AI-generated advice before applying it.</sub>\n")
	if marker := lineContentMarker(f.LineText); marker != "" {
		builder.WriteString(marker + "\n")
	}
	builder.WriteString(findingMarkerComment(FindingID(f)))
	return builder.String()
}

// FindingID is a stable identity for an inline finding. Path and new-line only:
// model titles drift across re-reviews and must not open a second thread.
func FindingID(f Finding) string {
	sum := sha256.Sum256([]byte(f.Path + "\n" + strconv.Itoa(f.Line)))
	return hex.EncodeToString(sum[:])[:findingIDHexChars]
}

func findingMarkerComment(id string) string {
	return findingMarkerPrefix + id + findingMarkerSuffix
}

func parseFindingTitle(body string) string {
	start := strings.Index(body, "**")
	if start < 0 {
		return ""
	}
	rest := body[start+2:]
	end := strings.Index(rest, "**")
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func normalizeFindingTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var builder strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			builder.WriteByte(c)
			space = false
			continue
		}
		if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func titlesLikelySame(a, b string) bool {
	a, b = normalizeFindingTitle(a), normalizeFindingTitle(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < 6 || len(b) < 6 {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func parseFindingID(body string) (string, bool) {
	start := strings.Index(body, findingMarkerPrefix)
	if start < 0 {
		return "", false
	}
	rest := body[start+len(findingMarkerPrefix):]
	end := strings.Index(rest, findingMarkerSuffix)
	if end < 0 {
		return "", false
	}
	id := rest[:end]
	if id == "" || !isHex(id) {
		return "", false
	}
	return strings.ToLower(id), true
}

func isHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func FormatWalkthrough(result AIReview, findings []Finding, notices []string, incremental bool, duration time.Duration, details ReviewDetails, style string, blockingSeverities []string) string {
	kind := "Full review"
	if incremental {
		kind = "Incremental review"
	}
	if len(blockingSeverities) == 0 {
		blockingSeverities = []string{"critical", "high"}
	}
	needsChanges := HasBlockingFinding(findings, blockingSeverities) || details.OpenPriorFindings > 0
	var builder strings.Builder
	builder.WriteString("## CodeReview walkthrough\n\n")
	builder.WriteString("### Verdict\n\n")
	if needsChanges {
		builder.WriteString("**Needs changes**\n\n")
		if details.OpenPriorFindings > 0 && !HasBlockingFinding(findings, blockingSeverities) {
			fmt.Fprintf(&builder, "%d finding(s) from earlier reviews remain open. This incremental pass did not re-evaluate those lines.\n\n", details.OpenPriorFindings)
		} else {
			builder.WriteString("Fix the high and critical findings below, then push. CodeReview will re-review this merge request automatically.\n\n")
		}
	} else {
		builder.WriteString("**Ready to merge**\n\n")
		builder.WriteString("No high or critical findings. A human should still read the diff before merge.\n\n")
	}
	fmt.Fprintf(&builder, "**%s · %s mode · complexity %d/5** · %s\n\n", kind, details.Mode, reviewComplexity(details, len(findings)), duration.Round(time.Second))
	if result.Summary != "" {
		builder.WriteString("### Summary\n\n")
		summaryLimit := 6000
		if style == "detailed" {
			summaryLimit = 12000
		}
		builder.WriteString(truncate(strings.TrimSpace(sanitizeModelText(result.Summary)), summaryLimit))
		builder.WriteString("\n\n")
	}
	if report := strings.TrimSpace(result.AgentReport); report != "" {
		builder.WriteString("### Adaptation report\n\n")
		if branch := strings.TrimSpace(result.BranchName); branch != "" {
			fmt.Fprintf(&builder, "Suggested branch: `%s`\n\n", sanitizeModelText(branch))
		}
		reportLimit := 16000
		if style == "detailed" {
			reportLimit = 24000
		}
		builder.WriteString(truncate(sanitizeModelText(report), reportLimit))
		builder.WriteString("\n\n")
	}
	if needsChanges {
		builder.WriteString("### Action items\n\n")
		n := 0
		for _, finding := range findings {
			if !HasBlockingFinding([]Finding{finding}, blockingSeverities) {
				continue
			}
			n++
			fmt.Fprintf(&builder, "%d. `%s:%d` — %s\n", n, escapeTable(sanitizeModelText(finding.Path)), finding.Line, escapeTable(sanitizeModelText(finding.Title)))
		}
		builder.WriteByte('\n')
	}
	if len(findings) == 0 {
		builder.WriteString("### Findings\n\nNo actionable findings met the configured confidence threshold.\n\n")
	} else {
		counts := map[string]int{}
		for _, finding := range findings {
			counts[finding.Severity]++
		}
		fmt.Fprintf(&builder, "### Findings\n\n**%d actionable:** %d critical · %d high · %d medium · %d low\n\n", len(findings), counts["critical"], counts["high"], counts["medium"], counts["low"])
		builder.WriteString("| Severity | Location | Finding |\n|---|---|---|\n")
		for _, finding := range findings {
			fmt.Fprintf(&builder, "| %s | `%s:%d` | %s |\n", finding.Severity, escapeTable(sanitizeModelText(finding.Path)), finding.Line, escapeTable(sanitizeModelText(finding.Title)))
		}
		builder.WriteByte('\n')
	}
	if len(result.Walkthrough) > 0 {
		if style == "compact" {
			builder.WriteString("<details><summary>Changed files</summary>\n\n")
		} else {
			builder.WriteString("### Changed files\n\n")
		}
		builder.WriteString("| File | Change | Risk |\n|---|---|---|\n")
		for _, item := range result.Walkthrough {
			fmt.Fprintf(&builder, "| `%s` | %s | %s |\n", escapeTable(sanitizeModelText(item.Path)), escapeTable(truncateInline(sanitizeModelText(item.Summary), 400)), escapeTable(sanitizeModelText(item.Risk)))
		}
		if style == "compact" {
			builder.WriteString("\n</details>\n\n")
		} else {
			builder.WriteByte('\n')
		}
	}
	if len(result.IssueAssessments) > 0 {
		builder.WriteString("<details><summary>Linked issue validation</summary>\n\n| Issue | Status | Assessment |\n|---|---|---|\n")
		for _, assessment := range result.IssueAssessments {
			fmt.Fprintf(&builder, "| `#%d` | %s | %s |\n", assessment.IID, escapeTable(sanitizeModelText(assessment.Status)), escapeTable(truncateInline(sanitizeModelText(assessment.Summary), 600)))
		}
		builder.WriteString("\n</details>\n\n")
	}
	if len(notices) > 0 {
		builder.WriteString("<details><summary>Review limits and notices</summary>\n\n")
		for _, notice := range notices {
			builder.WriteString("- ")
			builder.WriteString(notice)
			builder.WriteByte('\n')
		}
		builder.WriteString("\n</details>\n\n")
	}
	builder.WriteString("<details><summary>Review details</summary>\n\n")
	fmt.Fprintf(&builder, "- Provider: `%s`%s\n- Files/diff: `%d` / `%d chars`\n- Context: `%d files`, `%d tree entries`, `%d linked issues`\n- Model batches: `%d`; independent verification: `%t`; suppressed suggestions: `%d`\n", details.Provider, euLabel(details), details.Files, details.DiffChars, details.ContextFiles, details.TreeEntries, details.LinkedIssues, details.ModelBatches, details.Verification, details.SuppressedFindings)
	builder.WriteString("\n</details>\n\n")
	builder.WriteString("<sub>AI-generated review. Human verification remains required.</sub>\n")
	builder.WriteString(walkthroughMarker)
	return builder.String()
}

func appendWalkthroughMarker(body string) string {
	if strings.Contains(body, walkthroughMarker) {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n" + walkthroughMarker
}

func ownedWalkthroughNote(body string) bool {
	// Legacy notes used the heading before the product marker existed.
	return strings.Contains(body, walkthroughMarker) || strings.Contains(body, walkthroughHeading)
}

func latestOwnedWalkthroughID(notes []gitlab.Note) int64 {
	var latest int64
	for _, note := range notes {
		if note.ID > latest && ownedWalkthroughNote(note.Body) {
			latest = note.ID
		}
	}
	return latest
}

func reviewComplexity(details ReviewDetails, findings int) int {
	score := 1
	if details.Files > 4 || details.DiffChars > 12000 {
		score++
	}
	if details.Files > 12 || details.DiffChars > 40000 {
		score++
	}
	if details.Files > 30 || details.DiffChars > 90000 {
		score++
	}
	if findings > 5 || details.LinkedIssues > 2 {
		score++
	}
	if score > 5 {
		return 5
	}
	return score
}

func euLabel(details ReviewDetails) string {
	if details.Provider == "openrouter" && details.EURouting {
		return " (EU endpoint)"
	}
	return ""
}

func UpdateDescription(description, summary, mention string) string {
	block := summaryStart + "\n## Summary by CodeReview\n\n" + strings.TrimSpace(sanitizeModelText(summary)) + "\n" + summaryEnd
	if start := strings.Index(description, summaryStart); start >= 0 {
		if end := strings.Index(description[start:], summaryEnd); end >= 0 {
			end = start + end + len(summaryEnd)
			return description[:start] + block + description[end:]
		}
	}
	placeholder := "@" + mention + " summary"
	if index := strings.Index(strings.ToLower(description), strings.ToLower(placeholder)); index >= 0 {
		return description[:index] + block + description[index+len(placeholder):]
	}
	if strings.TrimSpace(description) == "" {
		return block
	}
	return strings.TrimRight(description, "\n") + "\n\n---\n\n" + block
}

func HasBlockingFinding(findings []Finding, severities []string) bool {
	blocking := map[string]struct{}{}
	for _, severity := range severities {
		blocking[strings.ToLower(severity)] = struct{}{}
	}
	for _, finding := range findings {
		if _, ok := blocking[strings.ToLower(finding.Severity)]; ok {
			return true
		}
	}
	return false
}

func validFindingPath(path string) bool {
	return path != "" && !strings.ContainsAny(path, "<>\n\r\x00")
}

func usableSuggestion(raw string) string {
	suggestion := strings.TrimSpace(raw)
	if suggestion == "" || len(suggestion) > 6000 || strings.Contains(suggestion, "```") || suggestionLooksLikeHTML(suggestion) {
		return ""
	}
	return sanitizeModelText(suggestion)
}

func safeSuggestion(finding Finding, addedLines map[int]string) string {
	if strings.TrimSpace(finding.Suggestion) == "" {
		return ""
	}
	if usableSuggestion(finding.Suggestion) == "" {
		return ""
	}
	lines := suggestionLines(finding.Suggestion)
	expected := 1
	if finding.EndLine > finding.Line {
		expected = finding.EndLine - finding.Line + 1
	}
	if len(lines) != expected {
		return ""
	}
	targets := make([]string, expected)
	for i := 0; i < expected; i++ {
		text, ok := addedLines[finding.Line+i]
		if !ok {
			return ""
		}
		targets[i] = text
	}
	if suggestionMatchesOtherAddedLine(finding.Suggestion, finding.Line, finding.Line+expected-1, addedLines) {
		return ""
	}
	for i, target := range targets {
		if isCommentOnly(target) && !isCommentOnly(lines[i]) {
			return ""
		}
	}
	suggestionText := strings.Join(lines, "\n")
	targetText := strings.Join(targets, "\n")
	if suggestionLooksLikeProse(suggestionText) {
		return ""
	}
	if sharesIdentifierToken(suggestionText, targetText) {
		return finding.Suggestion
	}
	if expected == 1 && isWholeCodeReplacement(targetText, suggestionText) {
		return finding.Suggestion
	}
	return ""
}

func suggestionLines(raw string) []string {
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func suggestionMatchesOtherAddedLine(suggestion string, start, end int, added map[int]string) bool {
	sug := strings.TrimSpace(strings.ReplaceAll(suggestion, "\r\n", "\n"))
	if sug == "" {
		return false
	}
	for n := start; n <= end; n++ {
		if strings.TrimSpace(added[n]) == sug {
			return false
		}
	}
	for n, text := range added {
		if n >= start && n <= end {
			continue
		}
		if strings.TrimSpace(text) == sug {
			return true
		}
	}
	return false
}

func isCommentOnly(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	return strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "--")
}

func suggestionLooksLikeProse(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	first := trimmed
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	lowerFirst := strings.ToLower(first)
	for _, prefix := range []string{
		"add a ",
		"the function",
		"you should",
		"please ",
		"consider ",
		"make sure",
		"do not ",
		"don't ",
	} {
		if strings.HasPrefix(lowerFirst, prefix) {
			return true
		}
	}
	if strings.Contains(trimmed, ". ") {
		return true
	}
	lowerAll := strings.ToLower(trimmed)
	if strings.Contains(lowerAll, "raise valueerror") && !strings.HasPrefix(lowerFirst, "raise ") {
		return true
	}
	return false
}

func sharesIdentifierToken(a, b string) bool {
	inB := identifierTokens(b)
	if len(inB) == 0 {
		return false
	}
	start := -1
	for i := 0; i <= len(a); i++ {
		if i < len(a) && isIdentChar(a[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= 2 {
			if _, ok := inB[a[start:i]]; ok {
				return true
			}
		}
		start = -1
	}
	return false
}

func identifierTokens(s string) map[string]struct{} {
	tokens := map[string]struct{}{}
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && isIdentChar(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= 2 {
			tokens[s[start:i]] = struct{}{}
		}
		start = -1
	}
	return tokens
}

func isIdentChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

func isWholeCodeReplacement(target, suggestion string) bool {
	if strings.ContainsAny(suggestion, "\n") || suggestionLooksLikeProse(suggestion) {
		return false
	}
	if !strings.Contains(target, "(") && !strings.Contains(target, "=") {
		return false
	}
	if strings.Contains(suggestion, "(") || strings.Contains(suggestion, "=") {
		return true
	}
	for _, keyword := range []string{"if ", "return ", "raise ", "def ", "func "} {
		if strings.Contains(suggestion, keyword) {
			return true
		}
	}
	return false
}

func validSeverity(value string) bool {
	return value == "critical" || value == "high" || value == "medium" || value == "low"
}

func severityRank(value string) int {
	return map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}[value]
}

func escapeTable(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " ")
}

func truncateInline(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
