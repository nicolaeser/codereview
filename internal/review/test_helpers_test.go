package review

import "github.com/nicolaeser/codereview/internal/gitlab"

func structDiff(path string) gitlab.Diff {
	return gitlab.Diff{OldPath: path, NewPath: path}
}
