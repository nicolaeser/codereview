package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolaeser/codereview/internal/ai"
	"github.com/nicolaeser/codereview/internal/config"
	"github.com/nicolaeser/codereview/internal/gitlab"
	"github.com/nicolaeser/codereview/internal/instructions"
	"github.com/nicolaeser/codereview/internal/logx"
	"github.com/nicolaeser/codereview/internal/state"
)

func TestMRDisableTokenMatchesTitleAndLabels(t *testing.T) {
	tokens := []string{"skip-codereview", "[skip review]"}
	tests := []struct {
		title  string
		labels []string
		want   string
		ok     bool
	}{
		{title: "Add helper", ok: false},
		{title: "[skip review] wip", want: "[skip review]", ok: true},
		{title: "SKIP-CODEREVIEW please", want: "skip-codereview", ok: true},
		{title: "normal", labels: []string{"skip-codereview"}, want: "skip-codereview", ok: true},
		{title: "normal", labels: []string{"team/skip-codereview"}, want: "skip-codereview", ok: true},
	}
	for _, test := range tests {
		got, ok := mrDisableToken(gitlab.MergeRequest{Title: test.title, Labels: test.labels}, tokens)
		if ok != test.ok || got != test.want {
			t.Fatalf("%q labels=%v = %q %t, want %q %t", test.title, test.labels, got, ok, test.want, test.ok)
		}
	}
}

func TestOwnedDiscussionsSkipAndStaleClassification(t *testing.T) {
	finding := Finding{Path: "main.go", Line: 3, Title: "Empty helper"}
	id := FindingID(finding)
	owned := ownedInlineDiscussions([]gitlab.Discussion{
		{
			ID: "keep",
			Notes: []gitlab.Note{{
				Body:       "n <!-- codereview-finding:" + id + " -->",
				Resolvable: true,
				Resolved:   false,
			}},
		},
		{
			ID: "stale-id",
			Notes: []gitlab.Note{{
				Body:       "old <!-- codereview-finding:deadbeefdeadbeef -->",
				Resolvable: true,
				Resolved:   false,
			}},
		},
		{
			ID: "resolved",
			Notes: []gitlab.Note{{
				Body:       "done <!-- codereview-finding:cccccccccccccccc -->",
				Resolvable: true,
				Resolved:   true,
			}},
		},
		{
			ID: "human",
			Notes: []gitlab.Note{{
				Body:       "human comment",
				Resolvable: true,
				Resolved:   false,
			}},
		},
	}, nil)
	if len(owned) != 3 {
		t.Fatalf("owned = %#v", owned)
	}
	unresolved := unresolvedFindingIDs(owned)
	if _, ok := unresolved[id]; !ok {
		t.Fatal("current finding should be treated as already open")
	}
	if _, ok := unresolved["cccccccccccccccc"]; ok {
		t.Fatal("resolved discussion must not block a new thread")
	}
	stale := staleOf(owned, finding)
	if len(stale) != 1 || stale[0].ID != "stale-id" {
		t.Fatalf("stale = %#v", stale)
	}
}

func TestStaleWhenFindingLineLeavesDiff(t *testing.T) {
	finding := Finding{Path: "main.go", Line: 3, Title: "Empty helper"}
	id := FindingID(finding)
	owned := []ownedDiscussion{{ID: "gone-line", FindingID: id, Unresolved: true}}
	stale := staleOf(owned, finding)
	if len(stale) != 0 {
		t.Fatalf("current still contains FindingID, stale = %#v", stale)
	}
}

func TestStaleWhenFindingAbsentThoughLineStillAdded(t *testing.T) {
	owned := []ownedDiscussion{{
		ID: "fixed-on-new-file", FindingID: "deadbeefdeadbeef",
		Path: "main.go", Line: 3, HasLocation: true, Unresolved: true,
	}}
	stale := staleOf(owned)
	if len(stale) != 1 || stale[0].ID != "fixed-on-new-file" {
		t.Fatalf("absent finding must be stale even on an added line, stale = %#v", stale)
	}
	other := Finding{Path: "main.go", Line: 8, Title: "Different helper"}
	stale = staleOf(owned, other)
	if len(stale) != 1 || stale[0].ID != "fixed-on-new-file" {
		t.Fatalf("different path/line must be stale, stale = %#v", stale)
	}
}

func staleOf(owned []ownedDiscussion, findings ...Finding) []ownedDiscussion {
	current := map[string]Finding{}
	for _, finding := range findings {
		current[FindingID(finding)] = finding
	}
	return staleOwnedDiscussions(owned, current, findings, nil, nil, false)
}

func TestPublishInlineSkipsWhenTitleChanges(t *testing.T) {
	existing := sampleInlineFinding()
	existing.Title = "Divide by Zero Error"
	current := sampleInlineFinding()
	current.Title = "possible division by zero"
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     current,
		discussions: []gitlab.Discussion{unresolvedDiscussion("dup", existing)},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0 after title drift", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0 when line still exists", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the existing finding thread after title drift")
	}
}

func TestPublishInlineSkipsWhenPositionMatchesOldMarker(t *testing.T) {
	finding := sampleInlineFinding()
	discussion := gitlab.Discussion{
		ID: "legacy",
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       "prior <!-- codereview-finding:deadbeefdeadbeef -->",
			Resolvable: true,
			Resolved:   false,
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: finding.Line},
		}},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0 when diff position still matches", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0 when the added line remains", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote when path+line matches a thread with an old finding marker")
	}
}

func TestPublishInlineSkipsDuplicateUnresolved(t *testing.T) {
	finding := sampleInlineFinding()
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{unresolvedDiscussion("dup", finding)},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the duplicate unresolved finding thread")
	}
}

func TestPublishInlineMatchesNeighborLineByContent(t *testing.T) {
	finding := sampleInlineFinding()
	leak := "func leak() {}"
	discussion := gitlab.Discussion{
		ID: "moved",
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       "prior\n" + lineContentMarker(leak) + "\n<!-- codereview-finding:deadbeefdeadbeef -->",
			Resolvable: true,
			Resolved:   false,
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: 4},
		}},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0 when line content matches a neighbor thread", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the content-matched thread")
	}
}

func TestLeftoverOpenCountKeepsUntouchedFinding(t *testing.T) {
	secret := ownedDiscussion{
		ID: "secret", FindingID: "aaaaaaaaaaaaaaaa", Path: "app.py", Line: 12,
		LineContentID: lineContentID("SECRET = 'x'"), HasLocation: true, Unresolved: true,
		Title: "Hardcoded secret",
	}
	full := []PreparedDiff{{
		Diff:         gitlab.Diff{NewPath: "app.py"},
		ChangedLines: map[int]struct{}{8: {}, 12: {}},
		AddedLines: map[int]string{
			8:  "if b == 0: raise ValueError()",
			12: "SECRET = 'x'",
		},
	}}
	guard := Finding{Path: "app.py", Line: 8, Title: "Unguarded divide", LineText: "if b == 0: raise ValueError()"}
	if n := leftoverOpenCount([]ownedDiscussion{secret}, newSkippedThreads(), []Finding{guard}, full); n != 1 {
		t.Fatalf("leftover = %d, want 1 for the untouched secret", n)
	}
	if n := leftoverOpenCount([]ownedDiscussion{secret}, newSkippedThreads(), nil, full); n != 1 {
		t.Fatalf("leftover with no current findings = %d, want 1", n)
	}
}

func TestStaleIncrementalDoesNotResolveUntouchedFinding(t *testing.T) {
	secret := ownedDiscussion{
		ID: "secret", FindingID: "aaaaaaaaaaaaaaaa", Path: "app.py", Line: 12,
		LineContentID: lineContentID("SECRET = 'x'"), HasLocation: true, Unresolved: true,
	}
	hunk := []PreparedDiff{{
		Diff:         gitlab.Diff{NewPath: "app.py"},
		ChangedLines: map[int]struct{}{8: {}},
		AddedLines:   map[int]string{8: "if b == 0: raise ValueError()"},
	}}
	full := []PreparedDiff{{
		Diff:         gitlab.Diff{NewPath: "app.py"},
		ChangedLines: map[int]struct{}{8: {}, 12: {}},
		AddedLines: map[int]string{
			8:  "if b == 0: raise ValueError()",
			12: "SECRET = 'x'",
		},
	}}
	stale := staleOwnedDiscussions([]ownedDiscussion{secret}, map[string]Finding{}, nil, hunk, full, true)
	if len(stale) != 0 {
		t.Fatalf("incremental hunk must not resolve an untouched finding, stale=%#v", stale)
	}
	stale = staleOwnedDiscussions([]ownedDiscussion{secret}, map[string]Finding{}, nil, hunk, hunk, true)
	if len(stale) != 1 {
		t.Fatalf("must resolve when content left the full diff, stale=%#v", stale)
	}
}

func TestIncrementalReviewKeepsPriorFindingsNeedsChanges(t *testing.T) {
	leak := Finding{
		Path: "main.go", Line: 3, Severity: "high", Category: "correctness",
		Title: "Empty helper", Body: "leak does nothing and may indicate incomplete work.", Confidence: 0.95,
		LineText: "func leak() {}",
	}
	stats := runInlineReview(t, inlineReviewOpts{
		noFindings:      true,
		incremental:     true,
		diff:            "@@ -0,0 +1,6 @@\n+package main\n+\n+func leak() {}\n+func helper() {}\n+\n+func main() {}\n",
		hunkDiff:        "@@ -3,2 +3,3 @@\n func leak() {}\n+func helper() {}\n \n",
		discussions:     []gitlab.Discussion{titledUnresolvedDiscussion("leak", leak, 3)},
		lastReviewedSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0 for leftover incremental findings", stats.resolves)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", stats.creates)
	}
	if !strings.Contains(stats.walkthrough, "**Needs changes**") {
		t.Fatalf("incremental leftover must stay Needs changes:\n%s", stats.walkthrough)
	}
	if strings.Contains(stats.walkthrough, "**Ready to merge**") {
		t.Fatalf("must not flip to Ready to merge:\n%s", stats.walkthrough)
	}
	if !strings.Contains(stats.walkthrough, "from earlier reviews remain open") {
		t.Fatalf("walkthrough must mention leftover findings:\n%s", stats.walkthrough)
	}
}

func TestFormatWalkthroughKeepsNeedsChangesWhenPriorOpen(t *testing.T) {
	body := FormatWalkthrough(AIReview{Summary: "guard only"}, nil, nil, true, time.Second, ReviewDetails{Mode: ModeStandard, OpenPriorFindings: 2}, "compact", []string{"critical", "high"})
	if !strings.Contains(body, "**Needs changes**") {
		t.Fatalf("expected Needs changes with leftover findings:\n%s", body)
	}
	if strings.Contains(body, "**Ready to merge**") {
		t.Fatal("must not flip to Ready to merge while prior findings remain")
	}
}

func TestPublishInlineResolvesWhenLineLeavesDiff(t *testing.T) {
	finding := sampleInlineFinding()
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		diff:        "@@ -1,3 +1,4 @@\n package main\n+func other() {}\n \n func main() {}\n",
		discussions: []gitlab.Discussion{unresolvedDiscussion("stale", finding)},
		force:       true,
	})
	if stats.resolves != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1", stats.resolves)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", stats.creates)
	}
}

func TestPublishInlineResolvesWhenBugFixedButFileStillNew(t *testing.T) {
	oldFinding := sampleInlineFinding()
	current := sampleInlineFinding()
	current.Line = 5
	current.Title = "Other helper"
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     current,
		diff:        "@@ -0,0 +1,7 @@\n+package main\n+\n+func leak() {}\n+\n+func other() {}\n+\n+func main() {}\n",
		discussions: []gitlab.Discussion{unresolvedDiscussion("old", oldFinding)},
		force:       true,
	})
	if stats.resolves != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1 for the fixed finding", stats.resolves)
	}
	if stats.creates != 1 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 1 for the new line", stats.creates)
	}
	if stats.updates != 0 {
		t.Fatalf("UpdateNote finding calls = %d, want 0", stats.updates)
	}
}

func TestPublishInlineBareMentionPostsHelpWithoutResolve(t *testing.T) {
	finding := sampleInlineFinding()
	discussion := unresolvedDiscussion("dup", finding)
	discussion.Notes = append(discussion.Notes, gitlab.Note{
		ID: 2, Body: "@codereview", Author: gitlab.User{Username: "dev"},
	})
	stats := runInlineReview(t, inlineReviewOpts{
		finding:         finding,
		discussions:     []gitlab.Discussion{discussion},
		lastReviewedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 on SHA skip", stats.aiCalls)
	}
	if stats.replies != 1 {
		t.Fatalf("ReplyToDiscussion calls = %d, want 1 help list", stats.replies)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0 for help", stats.resolves)
	}
}

func TestPublishInlineSkipsMentionOnAlreadyReviewedSHA(t *testing.T) {
	finding := sampleInlineFinding()
	stats := runInlineReview(t, inlineReviewOpts{
		finding:         finding,
		discussions:     []gitlab.Discussion{skipReplyDiscussion("dup", finding)},
		lastReviewedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 on SHA skip", stats.aiCalls)
	}
	if stats.replies != 1 {
		t.Fatalf("ReplyToDiscussion calls = %d, want 1", stats.replies)
	}
	if stats.resolves != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1", stats.resolves)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0", stats.creates)
	}
	if stats.updates != 0 {
		t.Fatalf("UpdateNote finding calls = %d, want 0", stats.updates)
	}
}

func titledUnresolvedDiscussion(id string, finding Finding, line int) gitlab.Discussion {
	finding.Line = line
	return gitlab.Discussion{
		ID: id,
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       FormatFinding(finding, "compact"),
			Resolvable: true,
			Resolved:   false,
			Author:     gitlab.User{Username: "codereview"},
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: line},
		}},
	}
}

func titledSkippedDiscussion(id string, finding Finding, line int) gitlab.Discussion {
	discussion := titledUnresolvedDiscussion(id, finding, line)
	discussion.Notes[0].Resolved = true
	discussion.Notes = append(discussion.Notes, gitlab.Note{
		ID:     2,
		Body:   SkipReplyBody(),
		Author: gitlab.User{Username: "codereview"},
	})
	return discussion
}

func threeLineNewFileDiff() string {
	return "@@ -0,0 +1,7 @@\n+package main\n+\n+func leak() {}\n+func helper() {}\n+func other() {}\n+\n+func main() {}\n"
}

func TestPublishInlineTitleDisambiguatesTwoNearbyThreads(t *testing.T) {
	leak := Finding{Path: "main.go", Line: 3, Severity: "high", Category: "correctness", Title: "Divide by zero", Body: "divides without a guard.", Confidence: 0.95, LineText: "func leak() {}"}
	other := Finding{Path: "main.go", Line: 5, Severity: "high", Category: "correctness", Title: "Hardcoded secret", Body: "secret in source.", Confidence: 0.95, LineText: "func other() {}"}
	current := other
	current.Line = 4
	stats := runInlineReview(t, inlineReviewOpts{
		finding: current,
		diff:    threeLineNewFileDiff(),
		discussions: []gitlab.Discussion{
			titledUnresolvedDiscussion("leak", leak, 3),
			titledUnresolvedDiscussion("secret", other, 5),
		},
		force: true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; title should pick the secret thread", stats.creates)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the title-matched nearby thread")
	}
}

func TestPublishInlineSkipDoesNotSuppressAdjacentDifferentFinding(t *testing.T) {
	leak := Finding{Path: "main.go", Line: 3, Severity: "high", Category: "correctness", Title: "Divide by zero", Body: "divides without a guard.", Confidence: 0.95, LineText: "func leak() {}"}
	other := Finding{Path: "main.go", Line: 4, Severity: "high", Category: "correctness", Title: "Hardcoded secret", Body: "secret in source.", Confidence: 0.95, LineText: "func helper() {}"}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     other,
		diff:        threeLineNewFileDiff(),
		discussions: []gitlab.Discussion{titledSkippedDiscussion("leak", leak, 3)},
		force:       true,
	})
	if stats.creates != 1 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 1 for a different adjacent finding", stats.creates)
	}
}

func TestPublishInlineSkipStaysStickyWhenTitleMatchesNearbyLine(t *testing.T) {
	leak := Finding{Path: "main.go", Line: 3, Severity: "high", Category: "correctness", Title: "Divide by zero", Body: "divides without a guard.", Confidence: 0.95, LineText: "func leak() {}"}
	current := leak
	current.Line = 4
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     current,
		diff:        threeLineNewFileDiff(),
		discussions: []gitlab.Discussion{titledSkippedDiscussion("leak", leak, 3)},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; titled skip must stay sticky on neighbor jitter", stats.creates)
	}
}

func TestPublishInlineSkipTitlePicksAmongTwoNearbySkips(t *testing.T) {
	leak := Finding{Path: "main.go", Line: 3, Severity: "high", Category: "correctness", Title: "Divide by zero", Body: "divides without a guard.", Confidence: 0.95, LineText: "func leak() {}"}
	other := Finding{Path: "main.go", Line: 5, Severity: "high", Category: "correctness", Title: "Hardcoded secret", Body: "secret in source.", Confidence: 0.95, LineText: "func other() {}"}
	current := leak
	current.Line = 4
	stats := runInlineReview(t, inlineReviewOpts{
		finding: current,
		diff:    threeLineNewFileDiff(),
		discussions: []gitlab.Discussion{
			titledSkippedDiscussion("leak", leak, 3),
			titledSkippedDiscussion("secret", other, 5),
		},
		force: true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; titled skip among two neighbors must hold", stats.creates)
	}
}

func TestPublishInlineUpdatesWhenGitLabLineIsNeighborWithoutContentMarker(t *testing.T) {
	finding := sampleInlineFinding()
	discussion := gitlab.Discussion{
		ID: "off-by-one",
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       "prior <!-- codereview-finding:deadbeefdeadbeef -->",
			Resolvable: true,
			Resolved:   false,
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: finding.Line + 1},
		}},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; unique neighbor line must update in place", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the unique neighbor-line thread")
	}
}

func TestPublishInlineUpdatesWhenModelReportsNeighborLine(t *testing.T) {
	finding := sampleInlineFinding()
	finding.Line = 4
	leak := "func leak() {}"
	discussion := gitlab.Discussion{
		ID: "moved",
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       "prior\n" + lineContentMarker(leak) + "\n<!-- codereview-finding:deadbeefdeadbeef -->",
			Resolvable: true,
			Resolved:   false,
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: 3},
		}},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		diff:        "@@ -0,0 +1,6 @@\n+package main\n+\n+func leak() {}\n+func helper() {}\n+\n+func main() {}\n",
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; neighbor line must update in place", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", stats.resolves)
	}
	if stats.updates == 0 {
		t.Fatal("expected UpdateNote on the content-neighbor thread")
	}
}

func TestPublishInlineSkipStaysStickyWhenModelReportsNeighborLine(t *testing.T) {
	finding := sampleInlineFinding()
	finding.Line = 4
	leak := "func leak() {}"
	discussion := gitlab.Discussion{
		ID: "skipped-neighbor",
		Notes: []gitlab.Note{
			{
				ID:         1,
				Body:       "prior\n" + lineContentMarker(leak) + "\n" + findingMarkerComment(FindingID(Finding{Path: "main.go", Line: 3})),
				Resolvable: true,
				Resolved:   true,
				Author:     gitlab.User{Username: "codereview"},
				Position:   &gitlab.DiffPosition{NewPath: "main.go", NewLine: 3},
			},
			{
				ID:     2,
				Body:   SkipReplyBody(),
				Author: gitlab.User{Username: "codereview"},
			},
		},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		diff:        "@@ -0,0 +1,6 @@\n+package main\n+\n+func leak() {}\n+func helper() {}\n+\n+func main() {}\n",
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; skipped neighbor must not clone", stats.creates)
	}
	if stats.updates != 0 {
		t.Fatalf("UpdateNote calls = %d, want 0 on a skipped thread", stats.updates)
	}
	if !strings.Contains(stats.walkthrough, "previously skipped") {
		t.Fatalf("walkthrough should note skipped findings, got:\n%s", stats.walkthrough)
	}
	if strings.Contains(stats.walkthrough, "Empty helper") {
		t.Fatalf("skipped finding must not stay actionable in the walkthrough:\n%s", stats.walkthrough)
	}
}

func TestTitleTokenSilentlyDisablesReview(t *testing.T) {
	stats := runInlineReview(t, inlineReviewOpts{
		finding: sampleInlineFinding(),
		title:   "[skip review] add helper",
	})
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 when the title disables review", stats.aiCalls)
	}
	if stats.creates != 0 || stats.updates != 0 || stats.disabledNotes != 0 {
		t.Fatalf("silent disable wrote creates=%d updates=%d ack=%d", stats.creates, stats.updates, stats.disabledNotes)
	}
}

func TestLabelTokenDisablesReview(t *testing.T) {
	stats := runInlineReview(t, inlineReviewOpts{
		finding: sampleInlineFinding(),
		labels:  []string{"codereview-skip"},
	})
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 when a label disables review", stats.aiCalls)
	}
	if stats.disabledNotes != 0 {
		t.Fatalf("silent label disable posted %d ack notes", stats.disabledNotes)
	}
}

func TestTitleTokenAckPostsOnce(t *testing.T) {
	stats := runInlineReview(t, inlineReviewOpts{
		finding: sampleInlineFinding(),
		title:   "skip-codereview: wip",
		skipAck: true,
	})
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0", stats.aiCalls)
	}
	if stats.disabledNotes != 1 {
		t.Fatalf("disabled ack notes = %d, want 1", stats.disabledNotes)
	}
	again := runInlineReview(t, inlineReviewOpts{
		finding: sampleInlineFinding(),
		title:   "skip-codereview: wip",
		skipAck: true,
		mrNotes: []gitlab.Note{{ID: 7, Body: "already " + disabledMarker}},
	})
	if again.disabledNotes != 0 {
		t.Fatalf("second ack notes = %d, want 0", again.disabledNotes)
	}
}

func TestForceMentionOverridesTitleDisable(t *testing.T) {
	stats := runInlineReview(t, inlineReviewOpts{
		finding:         sampleInlineFinding(),
		title:           "[skip review] add helper",
		force:           true,
		overrideDisable: true,
	})
	if stats.aiCalls == 0 {
		t.Fatal("force review must override title/label disable")
	}
}

func TestPublishInlineSkipStaysStickyAfterFullReviewLineJitter(t *testing.T) {
	finding := sampleInlineFinding()
	finding.Line = 4
	leak := "func leak() {}"
	discussion := gitlab.Discussion{
		ID: "skipped-old",
		Notes: []gitlab.Note{
			{
				ID:         1,
				Body:       "prior\n" + lineContentMarker(leak) + "\n" + findingMarkerComment(FindingID(Finding{Path: "main.go", Line: 3})),
				Resolvable: true,
				Resolved:   true,
				Author:     gitlab.User{Username: "codereview"},
				Position:   &gitlab.DiffPosition{NewPath: "main.go", NewLine: 3},
			},
			{
				ID:     2,
				Body:   SkipReplyBody(),
				Author: gitlab.User{Username: "codereview"},
			},
		},
	}
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		diff:        "@@ -0,0 +1,6 @@\n+package main\n+\n+\n+func leak() {}\n+\n+func main() {}\n",
		discussions: []gitlab.Discussion{discussion},
		force:       true,
	})
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0; skipped finding must not be cloned after line jitter", stats.creates)
	}
	if stats.updates != 0 {
		t.Fatalf("UpdateNote calls = %d, want 0 on a skipped thread", stats.updates)
	}
}

func TestPublishInlineSkipMentionDoesNotRecreateThread(t *testing.T) {
	finding := sampleInlineFinding()
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{skipReplyDiscussion("dup", finding)},
		force:       true,
	})
	if stats.replies != 1 {
		t.Fatalf("ReplyToDiscussion calls = %d, want 1", stats.replies)
	}
	if stats.resolves != 1 {
		t.Fatalf("ResolveDiscussion calls = %d, want 1", stats.resolves)
	}
	if stats.creates != 0 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 0 after skip", stats.creates)
	}
	if stats.updates != 0 {
		t.Fatalf("UpdateNote finding calls = %d, want 0 after skip", stats.updates)
	}
}

func TestPublishInlineSkipWithoutMentionIsIgnored(t *testing.T) {
	finding := sampleInlineFinding()
	discussion := unresolvedDiscussion("dup", finding)
	discussion.Notes = append(discussion.Notes, gitlab.Note{
		ID: 2, Body: "that's a false positive, skip", Author: gitlab.User{Username: "dev"},
	})
	stats := runInlineReview(t, inlineReviewOpts{
		finding:         finding,
		discussions:     []gitlab.Discussion{discussion},
		lastReviewedSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if stats.replies != 0 {
		t.Fatalf("ReplyToDiscussion calls = %d, want 0 without @mention", stats.replies)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0 without @mention", stats.resolves)
	}
	if stats.aiCalls != 0 {
		t.Fatalf("AI calls = %d, want 0 on SHA skip", stats.aiCalls)
	}
}

func TestPublishInlineDryRunSkipMentionDoesNotWrite(t *testing.T) {
	finding := sampleInlineFinding()
	stats := runInlineReview(t, inlineReviewOpts{
		finding:     finding,
		discussions: []gitlab.Discussion{skipReplyDiscussion("dup", finding)},
		dryRun:      true,
		force:       true,
	})
	if stats.replies != 0 || stats.resolves != 0 || stats.creates != 0 || stats.updates != 0 {
		t.Fatalf("dry-run wrote discussions replies=%d resolves=%d creates=%d updates=%d", stats.replies, stats.resolves, stats.creates, stats.updates)
	}
}

func TestPublishInlineFallsBackWhenListDiscussionsFails(t *testing.T) {
	stats := runInlineReview(t, inlineReviewOpts{
		finding:           sampleInlineFinding(),
		discussionsStatus: http.StatusInternalServerError,
		force:             true,
	})
	if stats.creates != 1 {
		t.Fatalf("CreateDiffDiscussion calls = %d, want 1 after list failure", stats.creates)
	}
	if stats.resolves != 0 {
		t.Fatalf("ResolveDiscussion calls = %d, want 0", stats.resolves)
	}
}

type stubAI struct {
	response string
	calls    *int32
}

func (s stubAI) Complete(context.Context, ai.Request) (string, error) {
	if s.calls != nil {
		atomic.AddInt32(s.calls, 1)
	}
	return s.response, nil
}

type inlineReviewOpts struct {
	finding           Finding
	diff              string
	hunkDiff          string
	discussions       []gitlab.Discussion
	discussionsStatus int
	force             bool
	dryRun            bool
	incremental       bool
	noFindings        bool
	lastReviewedSHA   string
	title             string
	labels            []string
	skipAck           bool
	mrNotes           []gitlab.Note
	overrideDisable   bool
}

type inlineReviewStats struct {
	creates       int32
	resolves      int32
	updates       int32
	replies       int32
	aiCalls       int32
	disabledNotes int32
	walkthrough   string
}

func sampleInlineFinding() Finding {
	return Finding{
		Path: "main.go", Line: 3, Severity: "high", Category: "correctness",
		Title: "Empty helper", Body: "leak does nothing and may indicate incomplete work.", Confidence: 0.95,
	}
}

func unresolvedDiscussion(id string, finding Finding) gitlab.Discussion {
	return gitlab.Discussion{
		ID: id,
		Notes: []gitlab.Note{{
			ID:         1,
			Body:       "prior\n" + findingMarkerComment(FindingID(finding)),
			Resolvable: true,
			Resolved:   false,
			Author:     gitlab.User{Username: "codereview"},
			Position:   &gitlab.DiffPosition{NewPath: finding.Path, NewLine: finding.Line},
		}},
	}
}

func skipReplyDiscussion(id string, finding Finding) gitlab.Discussion {
	discussion := unresolvedDiscussion(id, finding)
	discussion.Notes = append(discussion.Notes, gitlab.Note{
		ID:     2,
		Body:   "@codereview that's a false positive, skip",
		Author: gitlab.User{Username: "dev"},
	})
	return discussion
}

func runInlineReview(t *testing.T, opts inlineReviewOpts) inlineReviewStats {
	t.Helper()
	if opts.diff == "" {
		opts.diff = "@@ -1,3 +1,4 @@\n package main\n \n+func leak() {}\n func main() {}\n"
	}
	var stats inlineReviewStats
	title := opts.title
	if title == "" {
		title = "Add helper"
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/merge_requests/34"):
			writeJSON(response, map[string]any{
				"id": 340, "iid": 34, "project_id": 12, "title": title, "description": "demo",
				"state": "opened", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"web_url": "https://gitlab.example.test/group/proj/-/merge_requests/34",
				"labels":  opts.labels,
				"diff_refs": map[string]string{
					"base_sha":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"start_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					"head_sha":  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				},
				"author": map[string]any{"username": "dev"},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/repository/compare"):
			hunk := opts.hunkDiff
			if hunk == "" {
				hunk = opts.diff
			}
			writeJSON(response, map[string]any{
				"commit": map[string]any{"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				"diffs": []map[string]any{{
					"old_path": "main.go", "new_path": "main.go", "diff": hunk,
				}},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/diffs"):
			writeJSON(response, []map[string]any{{
				"old_path": "main.go", "new_path": "main.go", "diff": opts.diff,
			}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/merge_requests/34/discussions"):
			if opts.discussionsStatus != 0 && opts.discussionsStatus != http.StatusOK {
				http.Error(response, `{"message":"boom"}`, opts.discussionsStatus)
				return
			}
			writeJSON(response, opts.discussions)
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/notes"):
			if opts.mrNotes != nil {
				writeJSON(response, opts.mrNotes)
				return
			}
			writeJSON(response, []map[string]any{})
		case request.Method == http.MethodPost && strings.Contains(request.URL.Path, "/discussions/") && strings.HasSuffix(request.URL.Path, "/notes"):
			atomic.AddInt32(&stats.replies, 1)
			writeJSON(response, map[string]any{"id": 3, "body": "skipped"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/notes"):
			body := readNoteBody(request)
			if strings.Contains(body, disabledMarker) {
				atomic.AddInt32(&stats.disabledNotes, 1)
			} else {
				stats.walkthrough = body
			}
			writeJSON(response, map[string]any{"id": 99, "body": "walkthrough"})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/notes/"):
			body := readNoteBody(request)
			if strings.Contains(body, "codereview-finding") {
				atomic.AddInt32(&stats.updates, 1)
			}
			if strings.Contains(body, walkthroughMarker) || strings.Contains(body, walkthroughHeading) {
				stats.walkthrough = body
			}
			id := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
			writeJSON(response, map[string]any{"id": id, "body": "updated"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/discussions"):
			atomic.AddInt32(&stats.creates, 1)
			writeJSON(response, map[string]any{"id": "new", "notes": []map[string]any{{"id": 2}}})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/discussions/"):
			atomic.AddInt32(&stats.resolves, 1)
			writeJSON(response, map[string]any{"id": "stale"})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	instructionPath := filepath.Join(t.TempDir(), "INSTRUCTION.md")
	if err := os.WriteFile(instructionPath, []byte("Review for correctness."), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if opts.lastReviewedSHA != "" {
		if err := store.Update(12, 34, func(value *state.MRState) {
			value.LastReviewedSHA = opts.lastReviewedSHA
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		Target: config.TargetConfig{DryRun: opts.dryRun},
		Review: config.ReviewConfig{
			DefaultMode:         "standard",
			AutoIncremental:     true,
			PostSummary:         true,
			PostInline:          true,
			MaxDiffFiles:        10,
			MaxDiffChars:        20000,
			MaxComments:         12,
			MinimumConfidence:   0.1,
			CommentStyle:        "compact",
			Mention:             "codereview",
			MentionAliases:      []string{"codereview"},
			StatusReviewModes:   []string{"standard"},
			BlockingReviewModes: []string{"standard"},
			BlockingSeverities:  []string{"critical", "high"},
			SkipTokens:          []string{"skip-codereview", "codereview-skip", "no-codereview", "[skip review]", "[skip-codereview]"},
			SkipAck:             opts.skipAck,
		},
		AI: config.AIConfig{MaxInputChars: 20000, Provider: "openai"},
	}
	aiBody := inlineAIReviewJSON(opts.finding)
	if opts.noFindings {
		aiBody = `{"summary":"guard only","walkthrough":[{"path":"main.go","summary":"adds a guard","risk":"low"}],"findings":[]}`
	}
	ai := stubAI{response: aiBody, calls: &stats.aiCalls}
	svc := NewService(
		cfg,
		gitlab.NewWithOptions(server.URL+"/api/v4", "token", time.Second, false, 0),
		ai,
		instructions.Loader{DefaultPath: instructionPath},
		store,
		logx.New(io.Discard, "error"),
	)
	job := Job{ProjectID: 12, MergeRequest: 34, Full: !opts.incremental, Force: opts.force, Mode: "standard", Reason: "test"}
	if opts.overrideDisable {
		job.Force = true
	}
	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	return inlineReviewStats{
		creates:       atomic.LoadInt32(&stats.creates),
		resolves:      atomic.LoadInt32(&stats.resolves),
		updates:       atomic.LoadInt32(&stats.updates),
		replies:       atomic.LoadInt32(&stats.replies),
		aiCalls:       atomic.LoadInt32(&stats.aiCalls),
		disabledNotes: atomic.LoadInt32(&stats.disabledNotes),
		walkthrough:   stats.walkthrough,
	}
}

func inlineAIReviewJSON(finding Finding) string {
	payload, err := json.Marshal(AIReview{
		Summary:     "Adds a helper function.",
		Walkthrough: []WalkthroughItem{{Path: finding.Path, Summary: "adds helper", Risk: "low"}},
		Findings:    []Finding{finding},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func writeJSON(response http.ResponseWriter, payload any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(payload)
}
