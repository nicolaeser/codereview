# AI Context Index

This is the exhaustive discovery index for active `.ai/` Markdown context.

## Core

- [BASE.md](BASE.md) — Defines universal conduct, safety, context routing, and completion requirements and must be loaded before any repository work.
- [AI.md](AI.md) — Governs the `.ai/` system and must be loaded only when auditing or modifying repository context.
- [STYLE.md](STYLE.md) — Defines repository-owned presentation, GitLab Markdown, structured-log, accessibility, and documentation conventions and must be loaded for affected presentation work.

## Knowledge

- [Review Trust Boundaries](knowledge/review-trust-boundaries.md) — Maps authority, untrusted data, providers, webhook intake, and publication boundaries and must be loaded for work affecting prompts, provider routing, model output, GitLab mutations, or the listener.
- [Current Runtime Risks](knowledge/runtime-risks.md) — Records verified failure modes and contract gaps and must be loaded when diagnosing or changing CI delivery, state cache, identity, skip stickiness, configuration, provider endpoints, publication, or webhook behavior.
- [Dependency Bumps](knowledge/dependency-bumps.md) — Records default-off per-repo supply-chain review and GitLab Approve rules and must be loaded when changing dependency-only MR handling, bump yaml/env keys, or approve-on-clean vs approve-dependency-bumps.

## Instructions

- [Service Engineering](instructions/service-engineering.md) — Defines mandatory webhook listener, one-shot CLI, orchestration, state cache, and external-client rules and must be loaded for changes under `cmd/codereview`, `cmd/codereview-webhook`, or `internal`.
- [Security and Privacy](instructions/security-privacy.md) — Defines mandatory trust-boundary, token, secret, privacy, logging, webhook-secret, and external-write rules and must be loaded whenever those concerns are affected.
- [Operations and Delivery](instructions/operations.md) — Defines mandatory configuration, container, state cache, GitHub Actions, Dependabot, and image-release rules and must be loaded whenever those operational domains are affected.

## Playbooks

- [Change the Review Pipeline](playbooks/change-review-pipeline.md) — Provides the failure-window and state-transition procedure and must be loaded when changing the webhook or one-shot review path through persisted completion or exit codes.
- [Change Runtime Configuration](playbooks/change-runtime-configuration.md) — Provides the synchronized configuration and compatibility procedure and must be loaded when adding, removing, renaming, or changing a runtime setting or default.

## Personas

- [Application Security and Privacy Reviewer](personas/application-security-privacy-reviewer.md) — Applies an attacker, authority, and data-flow review and must be loaded when security- or privacy-sensitive work warrants a threat-boundary assessment.
- [Delivery Reliability Reviewer](personas/delivery-reliability-reviewer.md) — Applies an artifact-to-runtime and rollback trace and must be loaded when CI, image, deployment, probe, state-compatibility, or recovery work warrants release-readiness scrutiny.
- [External Contract Reviewer](personas/external-contract-reviewer.md) — Applies a wire-contract, compatibility, and side-effect review and must be loaded when GitLab or AI protocol changes warrant an integration assessment.
- [GitLab Experience Reviewer](personas/gitlab-experience-reviewer.md) — Applies a rendered-output, interaction, accessibility, and notification review and must be loaded when user-visible GitLab Markdown or command behavior changes.
- [Performance and Cost Reviewer](personas/performance-cost-reviewer.md) — Applies a request-amplification and resource-budget analysis and must be loaded when pipeline or runtime changes can materially affect latency, throughput, bounds, retention, or model cost.
