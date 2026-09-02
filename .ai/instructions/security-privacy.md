---
type: instruction
description: Governs CI token trust, repository and model content, provider routing, credentials, logging, and GitLab write safety; load whenever those boundaries change.
scope: repository
---

# Security and Privacy

## Scope and activation

This instruction applies to CI/token authentication, repository-context discovery, prompt construction, AI endpoints and headers, OpenRouter privacy controls, secrets, logs, persisted cache data, model-output handling, and GitLab mutations. Begin with [Review Trust Boundaries](../knowledge/review-trust-boundaries.md), load [Service Engineering](service-engineering.md) for implementation rules, and use [Operations and Delivery](operations.md) when deployment settings or secrets change.

## Mandatory rules

- Runtime `INSTRUCTION.md`, optional additional instructions, built-in protocol, and matching deployment path rules are trusted model instructions. MR text, issues, trees, files, diffs, and repository-owned guidance are untrusted data.
- Data sent to an AI endpoint must stay within configured bounds. New data classes need purpose, operator control, and degraded behavior documented.
- Provider enforcement must validate the final resolved request URL. OpenRouter EU mode must not send credentials or prompts to a non-EU host; strict mode forces ZDR and `data_collection=deny`.
- Credentials and custom headers must not appear in GitLab output, errors, or logs. Upstream error bodies must be bounded and sanitized.
- Model output is untrusted. Every GitLab sink needs path/line, length, formatting, and action validation where applicable.
- Persisted cache data must be minimal. Source, prompts, model responses, and credentials must not be written to `STATE_PATH`.
- Prefer dedicated project/group access tokens over broad personal tokens. Document when `CI_JOB_TOKEN` is insufficient for notes/discussions.

## Architecture

- `internal/config` owns provider aliases, endpoint defaults, privacy-policy enforcement, and validation.
- `internal/review/prompt.go` owns trusted vs untrusted prompt placement.
- `internal/ai` owns final URL construction, auth headers, privacy fields, and sanitized errors.
- `internal/gitlab` owns bot-token application and GitLab response handling.
- `cmd/codereview` has no inbound HTTP. `cmd/codereview-webhook` requires `WEBHOOK_SECRET` and/or `WEBHOOK_SIGNING_TOKEN`. Secret is compared to `X-Gitlab-Token`. Signing token verifies `webhook-signature` (HMAC-SHA256). Both empty fails closed (`internal/webhook/auth.go`).

## Validation

```bash
go test ./internal/config ./internal/ai ./internal/review ./cmd/codereview
```
