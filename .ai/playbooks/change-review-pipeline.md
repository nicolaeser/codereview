---
type: playbook
description: Safely changes webhook or one-shot review execution, context and model processing, GitLab output, exit codes, or persisted completion behavior.
---

# Change the review pipeline

## Use when

Use this playbook for changes to webhook or CLI entry, full or incremental review selection, context gathering, prompt batching, AI retries or verification, finding validation, GitLab publication, exit codes, or review-state completion.

## Required context

Load [Service Engineering](../instructions/service-engineering.md), [Review Trust Boundaries](../knowledge/review-trust-boundaries.md), and [Current Runtime Risks](../knowledge/runtime-risks.md). Also load [Security and Privacy](../instructions/security-privacy.md) for repository or prompt content, provider routing, logs, model output, or GitLab mutations. Inspect `cmd/codereview/main.go`, `internal/review/service.go`, `internal/review/prompt.go`, `internal/review/diff.go`, `internal/ai/`, `internal/gitlab/`, and `internal/state/store.go` as needed.

## Prerequisites

- Identify the initiating path: webhook payload, CLI flags, or local one-shot invocation.
- Identify affected modes (quick/standard/deep/security) and full vs incremental vs summary-only.
- Record how `LastReviewedSHA` and walkthrough IDs are read/written when state is present or missing.
- Define external side effects and required ordering before implementation.

## Ordered procedure

1. Trace one job from config/target resolution through context, model calls, validation, GitLab writes, state completion, and exit code mapping.
2. Build a failure-window table for config failure, cancellation, GitLab failure, AI failure, malformed model output, partial publication, blocking findings, and state-write failure.
3. Apply trust boundaries at every input and output transition.
4. Preserve mode semantics and input budgets, including verification passes and model fallback.
5. Validate model output against the prepared diff before publication.
6. Make the success boundary explicit: which GitLab side effects must succeed before the reviewed SHA advances; how blocking findings map to exit `2`.
7. Add focused regression tests using `httptest` / fakes through the real `cmd/codereview` `run` entry where exit codes or wiring change.
8. Update configuration docs and README when behavior, limits, modes, statuses, or contracts change.

## Validation

Run focused package tests, then the CI-equivalent Go gate in [Operations and Delivery](../instructions/operations.md).
