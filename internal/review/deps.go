package review

import "path"

// Known package-manager manifests and lockfiles (not Docker, CI, policy, or source).
var dependencyFilePatterns = []string{
	"go.mod",
	"go.sum",
	"package.json",
	"package-lock.json",
	"npm-shrinkwrap.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"bun.lock",
	"bun.lockb",
	"Cargo.toml",
	"Cargo.lock",
	"Gemfile",
	"Gemfile.lock",
	"composer.json",
	"composer.lock",
	"poetry.lock",
	"pdm.lock",
	"uv.lock",
	"Pipfile",
	"Pipfile.lock",
	"pyproject.toml",
	"requirements.txt",
	"requirements-*.txt",
	"mix.exs",
	"mix.lock",
	"pubspec.yaml",
	"pubspec.lock",
	"Podfile.lock",
	"Package.resolved",
}

// DependencyOnlyChange is true when every prepared diff is a known manifest or lockfile.
func DependencyOnlyChange(diffs []PreparedDiff) bool {
	if len(diffs) == 0 {
		return false
	}
	for _, diff := range diffs {
		if !dependencyFile(preparedDiffPath(diff)) {
			return false
		}
	}
	return true
}

// HasDependencyChange is true when any prepared diff is a known manifest or lockfile.
func HasDependencyChange(diffs []PreparedDiff) bool {
	for _, diff := range diffs {
		if dependencyFile(preparedDiffPath(diff)) {
			return true
		}
	}
	return false
}

// PolicyFileChanged is true when a prepared diff path is .codereview.yml or
// .codereview.yaml (repo root names only, or those exact basenames).
func PolicyFileChanged(diffs []PreparedDiff) bool {
	for _, diff := range diffs {
		if policyFile(preparedDiffPath(diff)) {
			return true
		}
	}
	return false
}

func dependencyBumpProtocol() string {
	return "\n\nThis diff is classified as a dependency/lockfile bump. Look for malicious module paths, replace/exclude/retract tricks, unexpected scripts (npm install hooks in package.json), widened version ranges, and anything that is not a routine version bump. Prefer no finding for a patch bump of a well-known module with no extra files. Do not approve in the JSON; findings only."
}

func preparedDiffPath(diff PreparedDiff) string {
	if diff.Diff.DeletedFile {
		return diff.Diff.OldPath
	}
	return diff.Diff.NewPath
}

func dependencyFile(diffPath string) bool {
	base := path.Base(diffPath)
	for _, pattern := range dependencyFilePatterns {
		if globMatch(pattern, diffPath) || globMatch(pattern, base) {
			return true
		}
	}
	return false
}

func policyFile(diffPath string) bool {
	base := path.Base(diffPath)
	return globMatch(".codereview.yml", diffPath) || globMatch(".codereview.yaml", diffPath) ||
		globMatch(".codereview.yml", base) || globMatch(".codereview.yaml", base)
}
