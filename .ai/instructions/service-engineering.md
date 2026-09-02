---
type: instruction
description: Governs changes to the webhook listener, one-shot CLI, configuration, review pipeline, state cache, and external client orchestration; load for any such change.
scope: repository
---

# Service Engineering

## Scope and activation

This instruction applies to `cmd/codereview`, `cmd/codereview-webhook`, all `internal/` packages, runtime configuration, review behavior, and GitLab or AI protocol integration. Also load [Security and Privacy](security-privacy.md) when the change affects a trust boundary, credential, prompt, log, or external write, and consult [Current Runtime Risks](../knowledge/runtime-risks.md) before changing delivery or state semantics. [Operations and Delivery](operations.md) is authoritative for configuration, packaging, containers, CI of this repository, and releases. Use the [review-pipeline playbook](../playbooks/change-review-pipeline.md) or [runtime-configuration playbook](../playbooks/change-runtime-configuration.md) when its activation condition matches.

## Mandatory rules

- Changes must preserve package ownership. `cmd/codereview-webhook` is the product composition root; `cmd/codereview` is the one-shot CLI and exit-code owner; protocol HTTP belongs in `internal/gitlab`, `internal/ai`, or `internal/webhook`; orchestration belongs in `internal/review`; optional cache persistence belongs in `internal/state`.
- Request contexts and configured timeouts must propagate through external calls. The one-shot CLI has no `ListenAndServe`. Inbound HTTP belongs only in `cmd/codereview-webhook`.
- Correctness-critical GitLab writes must return actionable failure to the orchestrator before advancing `LastReviewedSHA`. Inline discussions may remain best effort when documented as such.
- Runtime-configuration changes must follow [Operations and Delivery](operations.md) and the [runtime-configuration playbook](../playbooks/change-runtime-configuration.md).
- Prefer the standard library. Errors must retain operation context; [Security and Privacy](security-privacy.md) governs log and error content.
- Behavior changes need focused regression tests at the owning package. Exit-code behavior must be covered through the real `cmd/codereview` entry path (`run`). Empty `WEBHOOK_SECRET` must fail at `cmd/codereview-webhook` startup.

## Architecture and dependency boundaries

- `cmd/codereview/main.go` loads config, constructs clients, runs one review job, and maps results to exit codes `0` / `1` / `2`. It must not contain `ListenAndServe` or import `internal/webhook`.
- `cmd/codereview-webhook` is the product listener. The image `ENTRYPOINT` must start it.
- `internal/review` owns modes, diff preparation, bounded context, prompts, model-output validation, GitLab side-effect ordering, and state completion.
- `internal/gitlab` and `internal/ai` own wire formats, auth headers, response decoding, and transport errors.
- `internal/state` owns the optional JSON cache for last reviewed SHA and walkthrough note ids.
- `internal/instructions` owns loading trusted instruction files. Root `.ai/` documents are developer context only.

See [Configuration](../../docs/CONFIGURATION.md) for operator and project settings.

## Compatibility requirements

- GitLab integration must remain compatible with API v4 MR, note, discussion, repository, issue, compare, version, and commit-status contracts.
- Provider work must preserve OpenAI-compatible, Anthropic-compatible, and OpenRouter modes unless an intentional migration is documented.
- Incremental review must fall back to the full MR diff when compare data is unavailable.
- The one-shot CLI must continue to accept `CI_PROJECT_ID`, `CI_MERGE_REQUEST_IID`, `CI_API_V4_URL`, and `CI_JOB_TOKEN` fallbacks alongside explicit overrides.

## Prohibited patterns

- Do not add inbound HTTP to `cmd/codereview`.
- Do not mark a review complete or advance its SHA when a newly mandatory publication effect failed.
- Do not add unbounded queues, retries, log fields, or retained per-MR structures without bounds.
- Do not silently treat invalid configuration as success.

## Validation

```bash
go test ./internal/...
# CI-equivalent gate:
test -z "$(gofmt -l cmd internal)"
go vet ./...
go test -race -count=1 ./...
go build -trimpath -o /tmp/codereview ./cmd/codereview
go build -trimpath -o /tmp/codereview-webhook ./cmd/codereview-webhook
```
