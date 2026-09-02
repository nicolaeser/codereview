package review

import (
	"strings"
	"testing"
)

func TestSanitizeModelTextStripsScript(t *testing.T) {
	got := sanitizeModelText("<script>alert(1)</script>")
	if strings.Contains(strings.ToLower(got), "<script") {
		t.Fatalf("script tag remained: %q", got)
	}
}

func TestSanitizeModelTextStripsImageHandler(t *testing.T) {
	got := sanitizeModelText(`<img src=x onerror="alert(1)">`)
	if strings.Contains(strings.ToLower(got), "<img") || strings.Contains(got, "onerror") {
		t.Fatalf("img/onerror remained: %q", got)
	}
}

func TestSanitizeModelTextNeutralizesMentions(t *testing.T) {
	got := sanitizeModelText("ping @victim please")
	if strings.Contains(got, "@victim") {
		t.Fatalf("raw mention remained: %q", got)
	}
	if !strings.Contains(got, "@"+mentionZWSP+"victim") {
		t.Fatalf("expected neutralized mention, got %q", got)
	}
}

func TestSanitizeModelTextKeepsAllowedDetails(t *testing.T) {
	in := "<details><summary>more</summary>hidden</details>"
	got := sanitizeModelText(in)
	if got != in {
		t.Fatalf("allowed details changed: %q", got)
	}
}

func TestSanitizeModelTextStripsAttributesOnAllowedTags(t *testing.T) {
	got := sanitizeModelText(`<details onclick="alert(1)"><summary>x</summary>y</details>`)
	if strings.Contains(got, "onclick") || strings.Contains(got, "alert(1)") {
		t.Fatalf("event handler remained: %q", got)
	}
	if !strings.Contains(got, "<details>") || !strings.Contains(got, "<summary>x</summary>") {
		t.Fatalf("allowed tags were not preserved: %q", got)
	}
}

func TestSanitizeModelTextStripsHTMLComments(t *testing.T) {
	got := sanitizeModelText("before <!-- codereview-summary:end --> after")
	if strings.Contains(got, "<!--") || strings.Contains(got, "codereview-summary:end") {
		t.Fatalf("html comment remained: %q", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("surrounding text was lost: %q", got)
	}
}

func TestSanitizeModelTextKeepsMarkdown(t *testing.T) {
	in := "Use **bold**, `code`, and lists:\n\n- item\n\n| a | b |\n|---|---|\n| 1 | 2 |"
	if got := sanitizeModelText(in); got != in {
		t.Fatalf("markdown changed:\n%s", got)
	}
}

func TestSanitizeModelTextLeavesEmails(t *testing.T) {
	in := "Contact user@example.com for details."
	if got := sanitizeModelText(in); got != in {
		t.Fatalf("email changed: %q", got)
	}
}
