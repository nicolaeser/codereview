# Current Runtime Risks

## Applies when

Load this document when diagnosing failed reviews, incremental-skip surprises, partial GitLab publication, configuration mistakes, provider failures, container packaging, or webhook behavior.

These are verified properties or defects in the current implementation, not conventions to preserve.

## Delivery model

- **Product process is the webhook listener.** `cmd/codereview-webhook` is the image `ENTRYPOINT`. Empty `WEBHOOK_SECRET` fails closed. Overlapping deliveries return HTTP 200 busy. Merge-request and mention events for projects absent from the token user's membership list (`GET /projects?membership=true`, live refresh with cache, coalesced in-flight) return HTTP 200 `not a project member` without review work. Live changes replace the cache; unchanged or failed live lists keep it. A membership-list failure with no cache is fail-closed.
- **One-shot CLI.** `cmd/codereview` reviews a single merge request and exits. It has no inbound listener (`cmd/codereview/main.go`). `TestOneShotEntrypointHasNoHTTPServer` enforces that.
- **`GITLAB_TOKEN` is required** for notes/discussions. `CI_JOB_TOKEN` is often insufficient.
- **State is optional and local.** Missing or non-writable `STATE_PATH` still allows reviews, but incremental compare and walkthrough note reuse will not persist across restarts (`internal/state/store.go`). Persist it on a volume.
- **Blocking findings use exit code 2 on the one-shot CLI and do not advance `LastReviewedSHA`.** The webhook still returns HTTP 200 after accept. Runtime/config failures use exit code 1; success uses 0.

## Publication and identity

- **Canonical publication failures block SHA advancement.** Walkthrough creation (when enabled) and configured commit-status writes return errors before `LastReviewedSHA` advances; inline discussions remain best effort (`internal/review/service.go`).
- **Inline identity** is path + normalized added-line text (`<!-- codereview-line: -->`), then path+line, then a unique neighbor line (±1) with title disambiguation when two threads sit in that window. Jitter of two or more lines can still clone. Untitled legacy notes use unique-near only when there is exactly one neighbor.
- **Skip is sticky** for `<!-- codereview-skip -->` plus content-key and unique neighbor line with matching title. A skip must not suppress a differently titled adjacent finding.
- **Incremental leftover.** Resolve only findings whose line or content was in this hunk or is gone from the full MR diff. Open prior findings keep **Needs changes** (`leftoverOpenCount`). Must not treat “not in this hunk” as “fixed”.
- **Model Markdown reaches GitLab with selective validation.** Location/confidence/caps are enforced; summary and finding bodies can still contain links or HTML-like text (`internal/review/format.go`).

## Configuration and providers

- **Configuration is fail-closed at startup.** Invalid bounds, absolute AI path URLs, and missing GitLab/AI identity fail the process before review work starts (`internal/config/config.go`).
- **Yaml cannot set keys, URLs, or OpenRouter privacy.** Operator keys come from `AI_KEYS_FILE` or `AI_KEYS_JSON`. `AI_PROVIDER` is the offered list; yaml may pick only those names with a usable keys-file entry. `openrouter-eu` uses the EU host; `zdr` / `data_collection` are sent only when set on that keys-file entry.
- **`codereview login grok -h` / `--help` must print usage and exit 0 without starting device-code.** `login grok` without help still starts OAuth.
- **Dependency-bump extras are default off.** Yaml cannot self-enable approve if `.codereview.yml` is in the MR; project/group bots cannot approve; CodeReview never merges. `REVIEW_DEPENDENCY_BUMP_MODE` is also default off (`notify` / `wait` / `validate`). If operator `DEPENDENCY_BUMP_PUSH_URL` is set, a bump review that called a model POSTs repo, MR, branches, and the report there. Yaml cannot set the PUSH URL or secret; dry-run does not Approve or POST.
- **AI calls retry selected failures and try fallback models; GitLab calls are single-attempt.**
- **Title/label skip tokens** (`SKIP_MR_TOKENS`) disable review silently unless `SKIP_MR_ACK=true`. `@codereview review` / `--force` overrides title/label disable; description `@codereview ignore` does not.

## Context and compatibility

- **`AI_MAX_INPUT_CHARS` budgets base-plus-diff user content**, not the entire system instruction protocol (`internal/review/prompt.go`).
- **The product does not resolve the `.ai/` adapter system** of reviewed repositories; guideline globs fetch matched files as untrusted data only.
- **Build toolchains may differ:** CI derives Go from `go.mod`; the container build ARG may use a newer Go (`Dockerfile`, `go.mod`).

Use [Review Trust Boundaries](review-trust-boundaries.md) for data-flow trust, [Service Engineering](../instructions/service-engineering.md) for implementation changes, and [Operations and Delivery](../instructions/operations.md) for packaging and CI of this repository.
