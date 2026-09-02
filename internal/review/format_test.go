package review

import (
	"strings"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

func TestUpdateDescriptionIsStable(t *testing.T) {
	first := UpdateDescription("User text", "First summary", "codereview")
	second := UpdateDescription(first, "Second summary", "codereview")
	if strings.Count(second, summaryStart) != 1 || strings.Contains(second, "First summary") || !strings.Contains(second, "Second summary") {
		t.Fatalf("summary block was not replaced cleanly:\n%s", second)
	}
}

func TestCompactWalkthroughCollapsesDetail(t *testing.T) {
	result := AIReview{
		Summary:     "Short summary.",
		Walkthrough: []WalkthroughItem{{Path: "main.go", Summary: "Changes behavior", Risk: "medium"}},
		IssueAssessments: []IssueAssessment{{
			IID: 7, Status: "addressed", Summary: "Handled by the new branch.",
		}},
	}
	body := FormatWalkthrough(result, nil, nil, false, time.Second, ReviewDetails{Mode: ModeDeep, Provider: "openrouter", Files: 1}, "compact", nil)
	for _, expected := range []string{"### Verdict", "**Ready to merge**", "complexity 1/5", "<details><summary>Changed files</summary>", "Linked issue validation", "Review details", walkthroughMarker} {
		if !strings.Contains(body, expected) {
			t.Fatalf("compact walkthrough is missing %q:\n%s", expected, body)
		}
	}
}

func TestFormatWalkthroughIncludesMarkerAfterSanitize(t *testing.T) {
	result := AIReview{Summary: "see <!-- codereview-walkthrough --> @victim"}
	for _, style := range []string{"compact", "detailed"} {
		body := FormatWalkthrough(result, nil, nil, false, time.Second, ReviewDetails{Mode: ModeStandard, Provider: "openai", Files: 1}, style, nil)
		if strings.Count(body, walkthroughMarker) != 1 {
			t.Fatalf("%s marker count = %d:\n%s", style, strings.Count(body, walkthroughMarker), body)
		}
		if !strings.HasSuffix(strings.TrimSpace(body), walkthroughMarker) {
			t.Fatalf("%s walkthrough missing trailing marker:\n%s", style, body)
		}
		if strings.Contains(body, "&lt;!-- codereview-walkthrough") {
			t.Fatalf("%s marker was escaped:\n%s", style, body)
		}
		if !strings.Contains(body, walkthroughHeading) {
			t.Fatalf("%s missing walkthrough heading:\n%s", style, body)
		}
	}
}

func TestOwnedWalkthroughNoteMatchesMarkerOrLegacyHeading(t *testing.T) {
	if !ownedWalkthroughNote("intro\n" + walkthroughMarker) {
		t.Fatal("marker should be owned")
	}
	if !ownedWalkthroughNote(walkthroughHeading + "\n\nlegacy body") {
		t.Fatal("legacy heading should be owned")
	}
	if ownedWalkthroughNote("human comment about a review") {
		t.Fatal("unrelated note must not be owned")
	}
	notes := []gitlab.Note{
		{ID: 10, Body: walkthroughHeading + "\nolder"},
		{ID: 7, Body: "human"},
		{ID: 30, Body: "x " + walkthroughMarker},
		{ID: 20, Body: walkthroughHeading + "\nmiddle"},
	}
	if got := latestOwnedWalkthroughID(notes); got != 30 {
		t.Fatalf("latestOwnedWalkthroughID = %d, want 30", got)
	}
}

func TestCompactFindingOmitsConfidenceMetadata(t *testing.T) {
	body := FormatFinding(Finding{Severity: "high", Category: "security", Title: "Unsafe access", Body: "A caller can bypass the check.", Confidence: .99}, "compact")
	if strings.Contains(body, "confidence") || strings.Contains(body, "`security`") {
		t.Fatalf("compact finding contains detailed metadata: %s", body)
	}
}

func TestIssueAssessmentsRequireFetchedIssue(t *testing.T) {
	assessments := []IssueAssessment{
		{IID: 7, Status: "ADDRESSED", Summary: "implemented"},
		{IID: 8, Status: "addressed", Summary: "invented"},
	}
	got := ValidateIssueAssessments(assessments, []gitlab.Issue{{IID: 7}})
	if len(got) != 1 || got[0].IID != 7 || got[0].Status != "addressed" {
		t.Fatalf("validated assessments = %#v", got)
	}
}

func TestUpdateDescriptionReplacesPlaceholder(t *testing.T) {
	result := UpdateDescription("Before\n\n@codereview summary\n\nAfter", "Summary", "codereview")
	if strings.Contains(result, "@codereview summary") || !strings.Contains(result, summaryStart) {
		t.Fatalf("placeholder was not replaced:\n%s", result)
	}
}

func TestValidateFindingsRequiresChangedLine(t *testing.T) {
	diffs := []PreparedDiff{{ChangedLines: map[int]struct{}{5: {}}, Diff: structDiff("a.go")}}
	findings := []Finding{
		{Path: "a.go", Line: 5, Severity: "high", Title: "real", Body: "impact", Confidence: .9},
		{Path: "a.go", Line: 6, Severity: "high", Title: "wrong line", Body: "impact", Confidence: .9},
		{Path: "a.go", Line: 5, Severity: "low", Title: "low confidence", Body: "impact", Confidence: .2},
	}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || len(rejected) != 2 {
		t.Fatalf("got %d valid and %d rejected", len(valid), len(rejected))
	}
}

func TestValidateFindingsKeepsOneFindingPerPathLine(t *testing.T) {
	diffs := []PreparedDiff{{ChangedLines: map[int]struct{}{5: {}}, Diff: structDiff("a.go")}}
	findings := []Finding{
		{Path: "a.go", Line: 5, Severity: "high", Title: "first", Body: "impact", Confidence: .9},
		{Path: "a.go", Line: 5, Severity: "medium", Title: "second", Body: "impact", Confidence: .95},
	}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || valid[0].Title != "first" || len(rejected) != 1 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsRejectsUnsafePath(t *testing.T) {
	diffs := []PreparedDiff{{ChangedLines: map[int]struct{}{5: {}}, Diff: structDiff("a.go")}}
	findings := []Finding{
		{Path: "a.go\n<script>", Line: 5, Severity: "high", Title: "real", Body: "impact", Confidence: .9},
		{Path: "a.go<foo>", Line: 5, Severity: "high", Title: "real", Body: "impact", Confidence: .9},
	}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 0 || len(rejected) != 2 {
		t.Fatalf("got %d valid and %d rejected", len(valid), len(rejected))
	}
}

func TestValidateFindingsKeepsExactLineReplacement(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{12: {}},
		AddedLines:   map[int]string{12: "func leak() {}"},
		Diff:         structDiff("a.go"),
	}}
	findings := []Finding{{
		Path: "a.go", Line: 12, Severity: "high", Title: "leak", Body: "closes the helper",
		Suggestion: "func leak() {}", Confidence: .9,
	}}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || len(rejected) != 0 || valid[0].Suggestion != "func leak() {}" {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsStripsProseSuggestion(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{8: {}},
		AddedLines:   map[int]string{8: "print(divide(10, 0))"},
		Diff:         structDiff("a.go"),
	}}
	findings := []Finding{{
		Path: "a.go", Line: 8, Severity: "high", Title: "zero", Body: "division by zero",
		Suggestion: "Add a check for zero before division: if b == 0: raise ValueError(...)", Confidence: .9,
	}}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || len(rejected) != 0 || valid[0].Suggestion != "" {
		t.Fatalf("prose suggestion must be stripped: valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsStripsCrossWiredSuggestion(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{4: {}, 8: {}, 9: {}},
		AddedLines: map[int]string{
			4: "# secret",
			8: "print(divide(10, 0))",
			9: "print(divide(10,1))",
		},
		Diff: structDiff("a.go"),
	}}
	findings := []Finding{
		{
			Path: "a.go", Line: 4, Severity: "high", Title: "secret", Body: "leaked comment",
			Suggestion: "print(divide(10,1))", Confidence: .9,
		},
		{
			Path: "a.go", Line: 8, Severity: "high", Title: "zero", Body: "division by zero",
			Suggestion: "print(divide(10,1))", Confidence: .9,
		},
	}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 2 || len(rejected) != 0 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
	for _, finding := range valid {
		if finding.Suggestion != "" {
			t.Fatalf("cross-wired suggestion must be stripped: %#v", finding)
		}
	}
}

func TestValidateFindingsStripsCodeSuggestionOnComment(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{4: {}},
		AddedLines:   map[int]string{4: "# comment"},
		Diff:         structDiff("a.go"),
	}}
	findings := []Finding{{
		Path: "a.go", Line: 4, Severity: "medium", Title: "comment", Body: "not a defect line",
		Suggestion: "if err != nil { return err }", Confidence: .9,
	}}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || len(rejected) != 0 || valid[0].Suggestion != "" {
		t.Fatalf("code suggestion on comment must be stripped: valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsKeepsFindingWithoutSuggestion(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{5: {}},
		AddedLines:   map[int]string{5: "func leak() {}"},
		Diff:         structDiff("a.go"),
	}}
	findings := []Finding{{
		Path: "a.go", Line: 5, Severity: "high", Title: "real", Body: "impact", Confidence: .9,
	}}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || valid[0].Suggestion != "" || len(rejected) != 0 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsStripsSuggestionWhenAddedLineMissing(t *testing.T) {
	diffs := []PreparedDiff{{ChangedLines: map[int]struct{}{5: {}}, Diff: structDiff("a.go")}}
	findings := []Finding{{
		Path: "a.go", Line: 5, Severity: "high", Title: "real", Body: "impact",
		Suggestion: "func leak() {}", Confidence: .9,
	}}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 1 || valid[0].Suggestion != "" || len(rejected) != 0 {
		t.Fatalf("missing AddedLines must strip suggestion only: valid=%#v rejected=%#v", valid, rejected)
	}
}

func TestValidateFindingsKeepsMatchingEndLineSuggestion(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{5: {}, 6: {}},
		AddedLines: map[int]string{
			5: "func leak() {}",
			6: "func helper() {}",
		},
		Diff: structDiff("a.go"),
	}}
	kept := Finding{
		Path: "a.go", Line: 5, EndLine: 6, Severity: "high", Title: "leak", Body: "close helpers",
		Suggestion: "func leak() { return }\nfunc helper() { return }", Confidence: .9,
	}
	tooMany := Finding{
		Path: "a.go", Line: 6, Severity: "medium", Title: "helper", Body: "close helper",
		Suggestion: "func leak() { return }\nfunc helper() { return }", Confidence: .9,
	}
	valid, rejected := ValidateFindings([]Finding{kept, tooMany}, diffs, .78, 10)
	if len(valid) != 2 || len(rejected) != 0 {
		t.Fatalf("valid=%#v rejected=%#v", valid, rejected)
	}
	byLine := map[int]Finding{}
	for _, finding := range valid {
		byLine[finding.Line] = finding
	}
	if byLine[5].Suggestion != kept.Suggestion {
		t.Fatalf("matching EndLine suggestion was stripped: %#v", byLine[5])
	}
	if byLine[6].Suggestion != "" {
		t.Fatalf("multi-line suggestion without EndLine must be stripped: %#v", byLine[6])
	}
}

func TestValidateFindingsStripsNestedFenceAndHTMLSuggestions(t *testing.T) {
	diffs := []PreparedDiff{{
		ChangedLines: map[int]struct{}{5: {}, 6: {}},
		AddedLines: map[int]string{
			5: "func leak() {}",
			6: "func leak() {}",
		},
		Diff: structDiff("a.go"),
	}}
	findings := []Finding{
		{Path: "a.go", Line: 5, Severity: "high", Title: "fence", Body: "impact", Suggestion: "foo\n```\nbar", Confidence: .9},
		{Path: "a.go", Line: 6, Severity: "high", Title: "html", Body: "impact", Suggestion: `<script>alert(1)</script>`, Confidence: .9},
	}
	valid, rejected := ValidateFindings(findings, diffs, .78, 10)
	if len(valid) != 2 || len(rejected) != 0 {
		t.Fatalf("got %d valid and %d rejected", len(valid), len(rejected))
	}
	for _, finding := range valid {
		if finding.Suggestion != "" {
			t.Fatalf("unsafe suggestion was kept: %#v", finding)
		}
	}
}

func TestFindingIDStableAndSensitive(t *testing.T) {
	base := Finding{Path: "pkg/a.go", Line: 12, Title: "  Unsafe   Access "}
	same := Finding{Path: "pkg/a.go", Line: 12, Title: "unsafe access"}
	unicodeSame := Finding{Path: "pkg/a.go", Line: 12, Title: "\u00a0UNSAFE\taccess\n"}
	if got, want := FindingID(base), FindingID(same); got != want {
		t.Fatalf("normalized titles should share id: %s vs %s", got, want)
	}
	if got, want := FindingID(base), FindingID(unicodeSame); got != want {
		t.Fatalf("unicode-trimmed titles should share id: %s vs %s", got, want)
	}
	id := FindingID(base)
	if len(id) != 16 {
		t.Fatalf("id length = %d, want 16: %q", len(id), id)
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("id is not lowercase hex: %q", id)
		}
	}
	if FindingID(Finding{Path: "pkg/a.go", Line: 13, Title: "Unsafe Access"}) == id {
		t.Fatal("line change must change finding id")
	}
	if FindingID(Finding{Path: "pkg/a.go", Line: 12, Title: "Different title"}) != id {
		t.Fatal("title change must keep finding id for the same path and line")
	}
	if FindingID(Finding{Path: "pkg/b.go", Line: 12, Title: "Unsafe Access"}) == id {
		t.Fatal("path change must change finding id")
	}
}

func TestFormatFindingIncludesRawMarkerAfterSanitize(t *testing.T) {
	finding := Finding{
		Path:       "pkg/a.go",
		Line:       12,
		Severity:   "high",
		Title:      `Unsafe access <!-- spoof-finding:deadbeef --> @victim`,
		Body:       `impact <!-- wipe --> <script>alert(1)</script>`,
		Confidence: .9,
	}
	for _, style := range []string{"compact", "detailed"} {
		body := FormatFinding(finding, style)
		marker := findingMarkerComment(FindingID(finding))
		if !strings.Contains(body, marker) {
			t.Fatalf("%s finding missing raw marker %q:\n%s", style, marker, body)
		}
		if strings.Contains(body, "&lt;!-- codereview-finding:") {
			t.Fatalf("%s marker was escaped:\n%s", style, body)
		}
		start := strings.LastIndex(body, findingMarkerPrefix)
		if start < 0 {
			t.Fatalf("%s missing finding marker prefix:\n%s", style, body)
		}
		comment := body[start:]
		if end := strings.Index(comment, "-->"); end >= 0 {
			comment = comment[:end+3]
		}
		if strings.Contains(comment, "Unsafe access") || strings.Contains(comment, "spoof-finding") {
			t.Fatalf("%s marker embedded raw title or spoof:\n%s", style, comment)
		}
		if strings.Contains(body, "<!-- wipe -->") || strings.Contains(body, "<!-- spoof-finding:deadbeef -->") {
			t.Fatalf("%s model HTML comment survived sanitization:\n%s", style, body)
		}
		if id, ok := parseFindingID(body); !ok || id != FindingID(finding) {
			t.Fatalf("%s parseFindingID = %q ok=%t, want %s", style, id, ok, FindingID(finding))
		}
	}
}

func TestParseFindingIDUsesFirstMarker(t *testing.T) {
	id, ok := parseFindingID("intro <!-- codereview-finding:aaaaaaaaaaaaaaaa --> then <!-- codereview-finding:bbbbbbbbbbbbbbbb -->")
	if !ok || id != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("parseFindingID = %q ok=%t", id, ok)
	}
	if _, ok := parseFindingID("no marker here"); ok {
		t.Fatal("expected no finding id")
	}
}

func TestFormatFindingSanitizesModelHTMLAndMentions(t *testing.T) {
	body := FormatFinding(Finding{
		Severity:   "high",
		Title:      "ping @victim",
		Body:       `<script>alert(1)</script> <img src=x onerror="alert(1)"> <details><summary>more</summary>hidden</details>`,
		Confidence: .9,
	}, "compact")
	if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(body, "<img") || strings.Contains(body, "onerror") {
		t.Fatalf("unsafe html remained:\n%s", body)
	}
	if strings.Contains(body, "@victim") {
		t.Fatalf("raw mention remained:\n%s", body)
	}
	if !strings.Contains(body, "<details>") || !strings.Contains(body, "<summary>more</summary>") {
		t.Fatalf("allowed details were stripped:\n%s", body)
	}
	if !strings.Contains(body, "Verify AI-generated advice before applying it.") {
		t.Fatalf("missing human-verification disclosure:\n%s", body)
	}
}

func TestFormatFindingOmitsNestedFenceSuggestion(t *testing.T) {
	body := FormatFinding(Finding{
		Severity:   "medium",
		Title:      "replace",
		Body:       "unsafe suggestion",
		Suggestion: "foo\n```\nbar",
		Confidence: .9,
	}, "compact")
	if strings.Contains(body, "```suggestion") {
		t.Fatalf("nested fence suggestion was published:\n%s", body)
	}
}

func TestFormatFindingOmitsHTMLSuggestion(t *testing.T) {
	body := FormatFinding(Finding{
		Severity:   "medium",
		Title:      "replace",
		Body:       "unsafe suggestion",
		Suggestion: `<script>alert(1)</script>`,
		Confidence: .9,
	}, "compact")
	if strings.Contains(body, "```suggestion") || strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("html suggestion was published:\n%s", body)
	}
}

func TestFormatWalkthroughSanitizesModelTextAndEscapesTables(t *testing.T) {
	result := AIReview{
		Summary:     `<script>alert(1)</script> see @victim`,
		Walkthrough: []WalkthroughItem{{Path: "main.go", Summary: "uses a | pipe", Risk: "medium"}},
		IssueAssessments: []IssueAssessment{{
			IID: 7, Status: "addressed", Summary: "handled | still @victim",
		}},
		Findings: []Finding{{Path: "main.go", Line: 3, Severity: "high", Title: "a | b", Body: "impact", Confidence: .9}},
	}
	body := FormatWalkthrough(result, result.Findings, nil, false, time.Second, ReviewDetails{Mode: ModeDeep, Provider: "openrouter", Files: 1}, "compact", []string{"critical", "high"})
	if !strings.Contains(body, "**Needs changes**") || !strings.Contains(body, "### Action items") {
		t.Fatalf("high finding should request changes:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("script tag remained:\n%s", body)
	}
	if strings.Contains(body, "@victim") {
		t.Fatalf("raw mention remained:\n%s", body)
	}
	if !strings.Contains(body, "a \\| b") || !strings.Contains(body, "uses a \\| pipe") || !strings.Contains(body, "handled \\| still") {
		t.Fatalf("table pipes were not escaped:\n%s", body)
	}
	if !strings.Contains(body, "Human verification remains required.") {
		t.Fatalf("missing human-verification disclosure:\n%s", body)
	}
}

func TestUpdateDescriptionSanitizesSummaryAndKeepsMarkers(t *testing.T) {
	got := UpdateDescription("User text", "hi <!-- codereview-summary:end --> @victim <script>alert(1)</script>", "codereview")
	if strings.Count(got, summaryStart) != 1 || strings.Count(got, summaryEnd) != 1 {
		t.Fatalf("summary markers were not stable:\n%s", got)
	}
	if strings.Contains(got, "<!-- codereview-summary:end --> @victim") || strings.Contains(got, "@victim") || strings.Contains(strings.ToLower(got), "<script") {
		t.Fatalf("unsanitized model summary was published:\n%s", got)
	}
	if !strings.Contains(got, "User text") {
		t.Fatalf("user description was not preserved:\n%s", got)
	}
}
