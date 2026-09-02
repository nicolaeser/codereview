# Dependency-bump review and approve

## Applies when

Load this document when changing dependency-only merge-request classification, extra supply-chain review protocol, `.codereview.yml` `dependency_bumps` / `approve_dependency_bumps`, `REVIEW_DEPENDENCY_BUMPS`, `GITLAB_APPROVE_DEPENDENCY_BUMPS`, or GitLab Approve for those MRs.

## Contract

CodeReview reviews version-bump merge requests; it does not open them. Operator enablement is in [Configuration](../../docs/CONFIGURATION.md).

- `internal/review/deps.go` owns `DependencyOnlyChange` (package-manager manifests and lockfiles only; Dockerfile, CI, source, `.codereview.yml`, `renovate.json`, and `dependabot.yml` are not), `HasDependencyChange`, and `PolicyFileChanged`. The extra protocol is findings-only and must not approve in model JSON.
- `internal/config/repo_file.go` overlays `dependency_bumps`, `approve_dependency_bumps`, and `dependency_bump_mode`; env wins when set. Yaml cannot set PUSH URL/secret.
- `internal/review/bump.go` owns notify / wait / validate decisions and whether an operator PUSH URL should fire. `internal/review/approval.go` owns GitLab Approve and project-bot rejection.

Both extras **must** default off. Without env or yaml, extra protocol and dependency-only approve **must not** run; ordinary review of the MR still proceeds. Invalid yaml **must** fail closed before the model.

## Must

- Treat `REVIEW_DEPENDENCY_BUMPS` / `review.dependency_bumps` as the extra protocol gate for `DependencyOnlyChange` diffs.
- Treat `GITLAB_APPROVE_DEPENDENCY_BUMPS` / `review.approve_dependency_bumps` as GitLab Approve **only** for dependency-only + Ready to merge.
- Treat `REVIEW_DEPENDENCY_BUMP_MODE` / `review.dependency_bump_mode` as the incoming-bump handler: `notify` (no AI), `wait` (until `@mention`), `validate` (approve only if nothing big / lockfile-only; code-must-change blocks approve).
- If operator `DEPENDENCY_BUMP_PUSH_URL` is set, POST repo, MR, branches, and the adaptation report after a bump review that called a model. Yaml cannot set that URL or secret.
- Let env win when set, including explicit `false`.
- Ignore yaml `approve_dependency_bumps` and yaml-driven `validate` auto-approve when `PolicyFileChanged` is true, unless the matching operator env is set.
- Fail closed if approve is enabled and the token cannot approve.
- Keep `GITLAB_APPROVE_ON_CLEAN` as "any clean MR". Do not narrow or replace it with the dependency-only flag.

## Must not

- Must not merge. GitLab owns `approvals_required`, CODEOWNERS, and prevent-author-approval.
- Must not approve with a project or group bot token.
- Must not let repository yaml self-enable privileged approve on the MR that changes the policy file.
- Must not treat a CodeReview approve as merge.
- Must not open version-bump merge requests or click Merge.
