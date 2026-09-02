package review

import (
	"testing"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

func TestChangedNewLines(t *testing.T) {
	diff := `@@ -10,4 +10,6 @@ func run() {
 context
-old()
+newCall()
+
 unchanged
+last()
}`
	lines := changedNewLines(diff)
	for _, line := range []int{11, 12, 14} {
		if _, ok := lines[line]; !ok {
			t.Fatalf("expected new line %d in %#v", line, lines)
		}
	}
	if _, ok := lines[13]; ok {
		t.Fatalf("unchanged line 13 must not be marked changed: %#v", lines)
	}
	_, added := parseAddedLines(diff)
	if got := added[11]; got != "newCall()" {
		t.Fatalf("AddedLines[11] = %q, want %q", got, "newCall()")
	}
	prepared, _ := PrepareDiffs([]gitlab.Diff{{NewPath: "a.go", OldPath: "a.go", Diff: diff}}, nil, 10, 1000)
	if len(prepared) != 1 || prepared[0].AddedLines[11] != "newCall()" {
		t.Fatalf("PrepareDiffs AddedLines[11] = %#v", prepared)
	}
}

func TestPrepareDiffsFiltersAndLimits(t *testing.T) {
	diffs := []gitlab.Diff{
		{NewPath: "vendor/a.go", OldPath: "vendor/a.go", Diff: "@@ -1 +1 @@\n-a\n+b"},
		{NewPath: "src/a.go", OldPath: "src/a.go", Diff: "@@ -1 +1 @@\n-a\n+b"},
	}
	prepared, _ := PrepareDiffs(diffs, []string{"vendor/**"}, 10, 1000)
	if len(prepared) != 1 || prepared[0].Diff.NewPath != "src/a.go" {
		t.Fatalf("unexpected prepared diffs: %#v", prepared)
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"vendor/**", "vendor/lib/file.go", true},
		{"*.lock", "root.lock", true},
		{"*.lock", "nested/root.lock", false},
		{"**/*.lock", "nested/root.lock", true},
		{"src/?.go", "src/a.go", true},
	}
	for _, test := range tests {
		if got := globMatch(test.pattern, test.path); got != test.want {
			t.Errorf("globMatch(%q, %q) = %t, want %t", test.pattern, test.path, got, test.want)
		}
	}
}
