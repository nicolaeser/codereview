# Repository AI Context

Before starting any task, inspect the exhaustive [context index](SUMMARY.md) and load only the documents whose activation conditions match the work.

## Authority and conflict handling

- System and user instructions take precedence over repository context.
- This file contains universal repository rules. Applicable domain Instructions are mandatory; Knowledge records verified facts and risks; Playbooks add procedures; Personas add review scrutiny.
- Playbooks and Personas must not override this file or an applicable Instruction.
- Prefer current enforced configuration, tests, schemas, and repeated implementation patterns over stale prose. If authoritative sources materially conflict and the safe resolution is unclear, stop and ask.

## Universal conduct and safety

- Never claim or disclose that repository work was assisted or generated in commits, pull requests, changelogs, documentation, or code comments unless explicitly requested.
- Never add related `Co-authored-by`, attribution, or generated-by trailers.
- Legitimate product-domain references to external assistants, machine-readable product content, or similar functionality remain allowed when relevant to this repository.
- Do not create commits unless explicitly requested.
- Preserve unrelated files and existing user changes. Keep changes within the requested scope and avoid destructive operations.
- Inspect current implementations before adding patterns, abstractions, libraries, or dependencies.
- Prefer existing project conventions and authoritative sources. Do not turn a one-off implementation into a repository rule.
- Do not expose secrets, credentials, personal data, private paths, or captured runtime prompts, diffs, and upstream payloads through source, fixtures, documentation, or logs. Intentional repository-owned prompt templates and synthetic fixtures remain allowed when they contain no sensitive runtime data.
- Verify work with the repository's actual checks, proportionally to risk, and report checks that could not be run.

## Context selection

1. Classify the task and affected domains.
2. Inspect [SUMMARY.md](SUMMARY.md) for relevant existing documents.
3. Load applicable Knowledge and all mandatory domain Instructions.
4. Select a Playbook only when its repository-specific procedure closely matches the task.
5. Apply a Persona when its distinct review method, decision priorities, or expected output add useful scrutiny; related Instructions remain authoritative and do not make the Persona redundant.
6. Always load [STYLE.md](STYLE.md) for UI, design, responsive, interaction, motion, accessibility, GitLab-rendered Markdown, or other presentation work.
7. Ask about extending `.ai/` if a reusable context gap remains.
8. Apply validation and change-impact checks before completion.

Compact routes:

- For webhook listener, one-shot CLI, review pipeline, state cache, GitLab, AI-client, or test work, load [Service Engineering](instructions/service-engineering.md) and matching Knowledge indexed in `SUMMARY.md`.
- For authentication, tokens, secrets, untrusted content, privacy, logging, or external-data boundaries, also load [Security and Privacy](instructions/security-privacy.md).
- For configuration, containers, CI packaging, GitHub Actions, Dependabot, releases, or deployment examples, load [Operations and Delivery](instructions/operations.md).
- For changes to this context system, read [AI.md](AI.md); do not load it for ordinary project work.

During ordinary project work, if no existing `.ai/` document covers a reusable context need, ask whether `.ai/` should be extended. Do not extend it without explicit approval unless context maintenance is the task.

## Completion

- Re-read the affected diff and check for unintended files, stale references, unsafe disclosure, and unhandled change impact.
- Run the narrowest relevant tests first, then the applicable repository gate for the risk level.
- For source changes, use the CI-equivalent commands documented by the applicable Instruction rather than treating `make test` as the full gate.
- Run documentation link checks and `git diff --check` when documentation changes.
- State what changed, what was verified, and any unresolved risk. Do not claim success beyond the available evidence.
