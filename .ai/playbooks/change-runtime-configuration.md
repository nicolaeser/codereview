---
type: playbook
description: Safely adds or changes environment configuration, provider defaults, privacy controls, runtime limits, probes, or Compose deployment values.
---

# Change runtime configuration

## Use when

Use this playbook when adding, removing, renaming, or changing an environment variable, default, parser, validation rule, provider alias, authentication mode, privacy setting, timeout, limit, worker or queue setting, instruction path, state path, server route, probe, or Compose value.

## Required context

Load [Operations and Delivery](../instructions/operations.md), [Service Engineering](../instructions/service-engineering.md), and [Current Runtime Risks](../knowledge/runtime-risks.md). Also load [Security and Privacy](../instructions/security-privacy.md) when the setting affects authentication, authorization, secrets, privacy, provider routing, untrusted data, logs, persistence, or an external write. Then inspect the relevant configuration field, `Load` assignment, normalization, and `Validate` rule in `internal/config/config.go`, plus every consumer and the corresponding entries in `docker-compose.yml` and `docs/CONFIGURATION.md`.

## Prerequisites

- Classify the value as secret, trusted instruction, public identifier, endpoint, enum, boolean, numeric limit, duration, path, or compatibility alias.
- Record behavior for the variable when unset, empty, malformed, valid, and out of range.
- Identify existing deployed values and whether a rename or default change would alter behavior without an operator edit.
- For security-sensitive settings, identify the fail-closed value and any precedence relationship before implementation.

## Ordered procedure

1. Add or change the typed configuration field and its environment loading in `internal/config/config.go`; keep parsing, normalization, provider defaults, and validation responsibilities explicit.
2. Reject unsafe or nonsensical values at startup. A deliberate fallback must be testable and must not silently disable authentication, replay protection, timeouts, bounds, privacy, or deduplication.
3. Preserve provider alias normalization and authentication defaults. For OpenRouter, ensure strict privacy remains authoritative over lower-level ZDR and data-collection values, and keep EU routing constrained to the EU hostname.
4. Update `docker-compose.yml` with a safe non-secret example and maintain inheritance through `docker-compose.dev.yml`. Do not embed a real credential or local private path.
5. Update `docs/CONFIGURATION.md` and README examples so names, defaults, valid values, precedence, and operational consequences match executable behavior.
6. Add table-driven tests covering unset, empty, malformed, minimum, maximum, and conflicting values as applicable. Include a test proving the fail-closed result for each security-sensitive change.
7. Trace the value into its consumer and verify that zero, negative, very large, and disabled forms cannot panic, remove a required bound, bypass a check, or create an unintended no-timeout mode.
8. Validate both Compose definitions and start the local image when the change affects startup, paths, probes, state, networking, or container behavior.

## Change-impact analysis

Assess backwards compatibility, secret handling, provider routing and data policy, external API behavior, request timeouts, memory and prompt bounds, optional state cache paths, Compose inheritance, and operator rollback.

## Validation

- Run the CI-equivalent Go gate and both Compose validation commands in [Operations and Delivery](../instructions/operations.md#commands); run container startup checks only when the change affects startup or runtime behavior.
- Run focused configuration tests and the tests for every direct consumer.
- Inspect effective Compose configuration without printing real secrets into captured output.
- For startup-affecting changes, build and run the local container, check readiness, inspect startup logs, and separately test the affected external connection when safe credentials and an authorized environment are available.

## Rollback and recovery

- Retain the previous variable name or behavior for a documented compatibility window when existing deployments cannot change atomically.
- Record the previous effective configuration and immutable image before rollout; rollback must restore both together when their contracts are coupled.
- Preserve the state volume across configuration rollback. A state-path or format change requires an explicit copy, migration, and restore plan before deployment.
- Credential rollback must account for rotation overlap without logging or committing either value.

## Definition of done

- Executable defaults, parsing, validation, consumers, Compose values, and operator documentation agree.
- Invalid and security-sensitive cases fail safely and have regression tests.
- Both Compose definitions validate, the complete Go gate passes, and startup/readiness checks pass when runtime behavior changed.
- Deployment and rollback steps identify the effective configuration, immutable image, secret-rotation handling, and state-preservation requirements.
