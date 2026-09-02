# Review Trust Boundaries

## Applies when

Load this document when changing or reviewing webhook intake, one-shot CLI entry, repository-context collection, prompt construction, AI providers, GitLab writes, logging, or optional state cache.

## Boundary map

CodeReview is a review-only bridge between GitLab and a configured AI endpoint. The product process is `cmd/codereview-webhook`. It fetches repository material through the GitLab API and does not clone, build, or execute reviewed code (`internal/gitlab/client.go`, `internal/review/service.go`). The one-shot `cmd/codereview` CLI is debug-only and has no inbound HTTP.

The relevant trust boundaries are:

1. **Operator to process.** Environment variables, bind-mounted `AI_KEYS_FILE`, and CLI flags are operator-controlled configuration. Tokens (`GITLAB_TOKEN`, provider keys, `WEBHOOK_SECRET`) are secrets and must never be logged or published (`internal/config/config.go`, `internal/webhook/auth.go`).
2. **Operator instructions to model system input.** The required runtime `INSTRUCTION.md`, its optional addition, the built-in review protocol, and matching `PATH_INSTRUCTIONS_JSON` entries form the trusted model-instruction boundary (`internal/instructions/loader.go`, `internal/review/prompt.go`).
3. **Repository and merge-request material to model user input.** Titles, descriptions, labels, issue text, trees, files, diffs, and leftover `@mention` text are untrusted even when fetched from an authenticated GitLab instance (`internal/review/prompt.go`, `internal/review/commands.go`). Optional `.codereview.yml` may select a `provider` listed in `AI_PROVIDER` with a usable keys-file entry; yaml cannot set keys, URLs, or privacy.
4. **Service to AI provider.** The configured endpoint receives trusted instructions together with selected repository content. Named `openrouter-eu` uses `https://eu.openrouter.ai/api/v1`. `provider.zdr` and `provider.data_collection` are sent only when the operator set them on that keys-file entry (or via env when there is no keys-file entry). Custom OpenAI-compatible `base_url` values come only from the operator keys file.
5. **Model output to GitLab.** Findings are constrained to reviewed paths, added lines, confidence, severity, and count (`internal/review/format.go`).
6. **Webhook intake.** Inbound POSTs require `X-Gitlab-Token` equal to `WEBHOOK_SECRET`; an empty secret fails closed. The listener acknowledges HTTP 200 and reviews asynchronously. It must not appear in `cmd/codereview`. Delivery of an instance System Hook is not authorization: the listener reviews only projects in the token user's membership list.
7. **Service to local state and logs.** Optional JSON state stores last reviewed SHAs and walkthrough note IDs only. Local `codereview login` tokens live in `~/.codereview/auth.json`.

Apply the mandatory controls in [Security and Privacy](../instructions/security-privacy.md) whenever a change crosses one of these boundaries.
