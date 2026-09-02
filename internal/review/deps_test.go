package review

import (
	"strings"
	"testing"

	"github.com/nicolaeser/codereview/internal/gitlab"
)

func TestDependencyOnlyChange(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  bool
	}{
		{name: "go.mod and go.sum only", paths: []string{"go.mod", "go.sum"}, want: true},
		{name: "go.mod plus source", paths: []string{"go.mod", "main.go"}, want: false},
		{name: "empty", paths: nil, want: false},
		{name: "Dockerfile only", paths: []string{"Dockerfile"}, want: false},
		{name: "package-lock.json only", paths: []string{"package-lock.json"}, want: true},
		{name: "nested lockfile", paths: []string{"frontend/package-lock.json"}, want: true},
		{name: "requirements glob", paths: []string{"requirements-dev.txt"}, want: true},
		{name: "policy file", paths: []string{".codereview.yml"}, want: false},
		{name: "Dockerfile in subdir", paths: []string{"deploy/Dockerfile"}, want: false},
		{name: "pom.xml", paths: []string{"pom.xml"}, want: false},
		{name: "csproj", paths: []string{"App.csproj"}, want: false},
		{name: "renovate.json", paths: []string{"renovate.json"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var diffs []PreparedDiff
			for _, p := range test.paths {
				diffs = append(diffs, PreparedDiff{Diff: structDiff(p)})
			}
			if got := DependencyOnlyChange(diffs); got != test.want {
				t.Fatalf("DependencyOnlyChange(%v) = %t, want %t", test.paths, got, test.want)
			}
		})
	}
}

func TestHasDependencyChange(t *testing.T) {
	if HasDependencyChange(nil) {
		t.Fatal("empty diffs must not be a dependency change")
	}
	if !HasDependencyChange([]PreparedDiff{{Diff: structDiff("go.mod")}, {Diff: structDiff("main.go")}}) {
		t.Fatal("mixed diffs that include go.mod must be a dependency change")
	}
	if HasDependencyChange([]PreparedDiff{{Diff: structDiff("main.go")}}) {
		t.Fatal("source-only diffs must not be a dependency change")
	}
}

func TestPolicyFileChanged(t *testing.T) {
	diffs := []PreparedDiff{{Diff: structDiff(".codereview.yml")}}
	if !PolicyFileChanged(diffs) {
		t.Fatal("PolicyFileChanged(.codereview.yml) = false, want true")
	}
	if DependencyOnlyChange(diffs) {
		t.Fatal("DependencyOnlyChange(.codereview.yml) = true, want false")
	}
	nested := []PreparedDiff{{Diff: structDiff("team/.codereview.yaml")}}
	if !PolicyFileChanged(nested) {
		t.Fatal("PolicyFileChanged(team/.codereview.yaml) = false, want true")
	}
}

func TestDependencyOnlyChangeDeletedLockfile(t *testing.T) {
	diffs := []PreparedDiff{{Diff: gitlab.Diff{OldPath: "go.sum", NewPath: "go.sum", DeletedFile: true}}}
	if !DependencyOnlyChange(diffs) {
		t.Fatal("deleted go.sum should be dependency-only")
	}
}

func TestBuildReviewRequestsDependencyBumpProtocol(t *testing.T) {
	diffs := []PreparedDiff{{Diff: gitlab.Diff{NewPath: "go.mod", Diff: "@@ -1 +1 @@\n+ok"}}}
	mr := gitlab.MergeRequest{Title: "deps"}
	enabled := BuildReviewRequests("base", mr, diffs, RepositoryContext{}, nil, 20000, false, ModeStandard, true, "")
	disabled := BuildReviewRequests("base", mr, diffs, RepositoryContext{}, nil, 20000, false, ModeStandard, false, "")
	protocol := dependencyBumpProtocol()
	if len(enabled) != 1 || !strings.Contains(enabled[0].System, protocol) {
		t.Fatalf("enabled system prompt missing dependency bump protocol: %#v", enabled)
	}
	if len(disabled) != 1 || strings.Contains(disabled[0].System, protocol) {
		t.Fatalf("disabled system prompt must not include dependency bump protocol: %#v", disabled)
	}
}
