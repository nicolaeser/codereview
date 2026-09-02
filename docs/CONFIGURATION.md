# Configuration

CodeReview has two layers. The **operator** (the host that runs the container) owns secrets, GitLab access, and which AI providers projects may use. A **reviewed project** may only pick a provider from `AI_PROVIDER` that also has a usable keys-file entry, plus review style. Projects cannot set keys or URLs.

Durations use Go syntax: `30s`, `5m`, `1h`.

The image starts `codereview-webhook`. GitLab POSTs merge-request and comment events; the process reviews one job at a time in the background. The `codereview` CLI is debug only (`--project` / `--mr`) and does not listen.

Operator sample: [docker-compose.yml](../docker-compose.yml). Project samples: [examples/codereview.yml](../examples/codereview.yml), [examples/ai-keys.json](../examples/ai-keys.json), [examples/.codereviewignore](../examples/.codereviewignore). Names, defaults, and validation live in `internal/config/config.go`.

## Operator settings you must set

Copy [examples/ai-keys.json](../examples/ai-keys.json) on the host and put real keys there. Bind-mount that file. Do not put keys in the image, in Compose, or in `.codereview.yml`.

| Setting | What it does |
|---|---|
| `GITLAB_API_URL` | GitLab API v4 base URL the **container** can reach (not GitLab’s idea of `localhost` unless you add extra hosts) |
| `GITLAB_TOKEN` | Group access token, `api` scope. Used to read the MR and post notes. Prefer this over `CI_JOB_TOKEN` |
| `AI_PROVIDER` | CSV of providers this instance offers. First name is the default. Built-in names: `openai`, `anthropic`, `openrouter`, `openrouter-eu`, `xai`, `grok`, `codex`. Custom names are allowed. A project yaml may pick only a name from this list that also has a usable keys-file entry. |
| `AI_KEYS_FILE` | JSON credentials. Image default `/data/ai-keys.json`. On start the process writes an object per name in `AI_PROVIDER` and removes names that are no longer listed. Existing values are kept. Optional `model` / `models` on an entry is the operator default for that provider. Max 64KiB. |
| `AI_KEYS_JSON` | Same JSON in the environment. If set, it wins over the file and the file is not rewritten. Not valid in project yaml |
| `AI_MODEL` / `AI_MODELS` | Instance default for the first `AI_PROVIDER` name. Per-provider defaults belong in the keys file. Yaml `model` still wins. `xai` and `grok` fall back to `grok-4.6` |
| `WEBHOOK_SECRET` | Shared secret. GitLab sends it as `X-Gitlab-Token`. Optional if `WEBHOOK_SIGNING_TOKEN` is set |
| `WEBHOOK_SIGNING_TOKEN` | GitLab signing token (`whsec_…`). Verifies `webhook-signature`. Optional if `WEBHOOK_SECRET` is set. At least one is required |
| `GITLAB_BOT_USERNAME` | Text used in published Markdown (`@codereview`). The GitLab note **author** is still the token owner |

`AI_API_KEY` is an optional single key for the default provider. Prefer `AI_KEYS_FILE` when `AI_PROVIDER` lists more than one name.

`xai` and `grok` both call `api.x.ai`. `xai` uses a metered API key. `grok` is SuperGrok / X Premium+ subscription login (`codereview login grok` on a laptop). The webhook process uses `AI_KEYS_FILE` / `AI_KEYS_JSON`, not home login files.

Invite `@codereview` as Developer on the group. Events for projects that user is not a member of return HTTP 200 (`not a project member`) and do not call a model.

## Webhook

Compose publishes `127.0.0.1:8090` and sets `WEBHOOK_LISTEN=0.0.0.0:8090` inside the container. Without Compose the binary binds `127.0.0.1:8090`.

Create a **group webhook** (Merge requests + Comments) to `http://<host>:8090/gitlab-webhook` with the same secret. Allow the URL under Admin → Settings → Network → Outbound requests. **System Hooks do not send comments**, so `@mentions` never arrive that way.

| Setting | Default | What it does |
|---|---:|---|
| `WEBHOOK_LISTEN` | `127.0.0.1:8090` | Bind address |
| `WEBHOOK_SECRET` | empty | Compared to `X-Gitlab-Token`. Required unless `WEBHOOK_SIGNING_TOKEN` is set |
| `WEBHOOK_SIGNING_TOKEN` | empty | HMAC of `{webhook-id}.{timestamp}.{body}`. GitLab header `webhook-signature` |
| `WEBHOOK_PATH` | `/gitlab-webhook` | POST path |
| `WEBHOOK_MAX_BODY` | `1048576` | Max JSON body; larger requests are rejected |
| `WEBHOOK_HANDLE_MR` | `true` | Handle merge-request open/update |
| `WEBHOOK_HANDLE_NOTES` | `true` | Handle comments (`@mentions`) |

GitLab waits about 10s and does not retry `429`/`500`. The process returns HTTP 200 and reviews in the background. A second overlapping delivery is skipped (`200` busy). `GET /health` returns `ok` without the secret.

Keep `STATE_PATH` on a volume so “already reviewed this SHA” survives restarts. Merge-request events do not force a re-review; they skip when that store already recorded the head.

## Project file: `.codereview.yml`

Optional. Fetched from the merge request head SHA (`.codereview.yml`, then `.codereview.yaml`). A missing file is a no-op. Invalid YAML, an oversized file, or a value that does not fit the schema fails **before** any model call.

This file may choose a `provider` from `AI_PROVIDER` (with a usable keys-file entry) and a `model` (or `models`). It cannot set API keys, GitLab or AI URLs, OpenRouter privacy, instruction paths, `JOB_TIMEOUT`, `STATE_PATH`, or `WEBHOOK_*`. Unknown keys are ignored.

Sample: [examples/codereview.yml](../examples/codereview.yml). Put it at the **repository root** as `.codereview.yml`.

| Key under `review:` | What it does |
|---|---|
| `provider` | Named provider from `AI_PROVIDER` with a usable keys-file entry. Does not set a URL or key |
| `model` | One model id. Wins over instance `AI_MODEL` |
| `models` | Ordered fallbacks. Wins over `AI_MODELS`. Do not set both `model` and `models` |
| `mode` | `quick`, `standard`, `deep`, or `security` |
| `drafts` | Review draft merge requests |
| `ignore_paths` | Glob denylist; merged with operator `IGNORE_PATHS` |
| `mention` | Label in published Markdown (`BOT_MENTION`) |
| `mention_aliases` | Extra labels for commands |
| `skip_tokens` | Extra title/label words that disable review |
| `skip_ack` | If true, post one “review disabled” note instead of staying silent |
| `path_instructions` | `{path, instructions}` extra focus for matching files |
| `dependency_bumps` | Extra supply-chain protocol when the diff is only manifests/lockfiles. Default off |
| `approve_dependency_bumps` | GitLab Approve only for those dependency-only MRs that are Ready to merge. Default off. Does not merge |
| `dependency_bump_mode` | How to handle a version-bump merge request: `notify`, `wait`, or `validate`. Default off. Yaml cannot set the outbound webhook URL or secret |

**Who wins.** Provider and model in yaml win over instance defaults when set, if the provider is in `AI_PROVIDER` and the keys-file entry is usable (`grok`/`codex` via login, others a non-empty key, custom names a `base_url`). For other keys, process environment and CLI flags win when set; yaml fills in when those are unset. `ignore_paths` and `path_instructions` **merge** (project first, then operator). `--mode` / `REVIEW_MODE` still win over yaml `mode`. Env `REVIEW_DEPENDENCY_BUMPS`, `GITLAB_APPROVE_DEPENDENCY_BUMPS`, and `REVIEW_DEPENDENCY_BUMP_MODE` win if set. Yaml approve / yaml `validate` auto-approve are ignored when the same MR also changes `.codereview.yml`, unless the matching operator env is set. Outbound bump webhooks use operator `DEPENDENCY_BUMP_PUSH_URL` only.

## Ignore file

Optional `.codereviewignore` at the repository root (fallback `.codereview/ignore`), fetched from the reviewed SHA. One glob per line; `#` comments and blank lines are ignored. A missing file is a no-op. Patterns are prepended to yaml `ignore_paths` and operator `IGNORE_PATHS`. Sample: [examples/.codereviewignore](../examples/.codereviewignore).

## How a review runs

`quick` uses less context and stays incremental when possible. `standard` is the default. `deep` and `security` always review the full MR diff, then run a second verification pass on the same bounded GitLab data. They are not a multi-agent fleet.

| Setting | Default | What it does |
|---|---:|---|
| `DEFAULT_REVIEW_MODE` | `standard` | Fallback mode |
| `REVIEW_MODE` / `--mode` | that default | `quick`, `standard`, `deep`, or `security` |
| `REVIEW_FULL` / `--full` | `false` | Always use the complete MR diff |
| `REVIEW_FORCE` / `--force` | `false` | Re-review even if this head SHA was already recorded |
| `REVIEW_DRY_RUN` / `--dry-run` | `false` | Fetch and call the model, but do not write to GitLab or advance the SHA cache |
| `REVIEW_DRAFTS` | `false` | Review draft MRs |
| `AUTO_INCREMENTAL_REVIEW` | `true` | Compare from the last reviewed SHA when possible |
| `JOB_TIMEOUT` | `20m` | Wall-clock budget for one review. Must be positive; `0` is rejected |
| `DEEP_REVIEW_VERIFICATION` | `true` | Second pass for deep/security |
| `COMMENT_STYLE` | `compact` | `compact` or `detailed` |
| `BOT_MENTION` | `codereview` | `@mention` name and Markdown label |
| `SKIP_MR_TOKENS` | `skip-codereview`, … | Title substring or GitLab label that disables the MR. Default is silent |
| `SKIP_MR_ACK` | `false` | If true, post one disabled-review note |
| `IGNORE_AUTHORS` | empty | Skip these GitLab usernames |
| `IGNORE_PATHS` | common build artifacts | Operator glob denylist |
| `MAX_DIFF_FILES` / `MAX_DIFF_CHARS` | `100` / `180000` | Diff budget |
| `MAX_INLINE_COMMENTS` | `12` | Maximum accepted findings |
| `MINIMUM_CONFIDENCE` | `0.78` | Drop findings below this |
| `POST_WALKTHROUGH` | `true` | Summary note on the MR. When on, a note is written **before** the model call; GitLab rejection fails the job |
| `POST_INLINE_COMMENTS` | `true` | Positioned discussions on added lines |
| `SUMMARY_IN_DESCRIPTION` | `false` | Marker-delimited block in the MR description |
| `BLOCK_ON_FINDINGS` | `false` | One-shot CLI exit `2` when blocking severities match (webhook still returns HTTP 200) |
| `BLOCKING_SEVERITIES` | `critical,high` | Severities that count as blocking |
| `STATE_PATH` | `/data/state.json` in Compose | Small JSON cache of last SHA and walkthrough note id. No source, prompts, or model output |

A bare `@codereview` prints the command list. Useful commands: `skip`, `review`, `full-review`, `ask`, `resolve`, `help`. Mention leftover text is untrusted and cannot replace `INSTRUCTION.md`.

## AI providers

| Provider | Protocol | Credential |
|---|---|---|
| `openai` | Chat Completions | key in `AI_KEYS_FILE` / `AI_API_KEY` |
| `anthropic` | Messages | same |
| `openrouter` | OpenRouter (`openrouter.ai`) | instance key; privacy from env unless the keys-file entry sets `zdr` / `data_collection` |
| `openrouter-eu` | OpenRouter (`eu.openrouter.ai`) | same protocol, EU host. `zdr` and `data_collection` are **omitted** from the request unless you set them on that keys-file entry |
| `xai` | xAI API | metered API key |
| `grok` | same API via subscription OAuth | `codereview login grok`; empty string in the keys file is normal |
| `codex` | ChatGPT subscription OAuth | `codereview login codex` |
| custom name | OpenAI Chat Completions | list it in `AI_PROVIDER`; object in the keys file with `key` and `base_url` |

New placeholders are objects. A string value is still accepted as the key. Yaml may select a name from `AI_PROVIDER` that has a usable keys-file entry; it cannot set keys, URLs, or privacy. `.codereview.yml` is optional; operator model defaults should live in this file so projects are not forced to set `model`.

```json
{
  "openrouter-eu": { "key": "sk-or-v1-replace-me", "model": "openai/gpt-4.1-mini" },
  "grok": { "key": "", "model": "grok-4.6" },
  "vllm": { "key": "", "base_url": "http://vllm:8000/v1", "model": "local-model" }
}
```

OpenRouter EU privacy is optional on that object: `zdr`, `data_collection` (`allow` or `deny`). If omitted, the EU host is used and those fields are not sent.

If `zdr` / `data_collection` are absent on that entry, the EU host is still used and those fields are not sent. Env `OPENROUTER_PRIVACY_STRICT` still applies when there is **no** keys-file entry for that provider.

Usual knobs: `AI_TIMEOUT` (default `180s`), `AI_MAX_RETRIES` (`2`), `AI_MAX_COMPLETIONS` (`8`), `AI_MAX_TOKENS` (`6000`), `AI_TEMPERATURE` (`0.1`), `AI_JSON_MODE` (`prompt`), `AI_JSON_REPAIR` (`true`). `AI_BASE_URL` is instance-only for built-in providers. Custom endpoints take `base_url` from the keys file only.

## Version bumps

These settings apply when a merge request changes package manifests or lockfiles. CodeReview does not open those merge requests and does not merge them. Valid modes are `notify`, `wait`, and `validate`. Anything else fails closed.

| Mode | Model | GitLab Approve | `DEPENDENCY_BUMP_PUSH_URL` |
|---|---|---|---|
| unset / `off` | Ordinary review | Only if you also enabled approve flags | After a model review, if the URL is set |
| `notify` | No | No | No |
| `wait` | No until `@mention` / `--force` | No | After that review, if the URL is set |
| `validate` | Yes | Yes, only when the diff is manifests/lockfiles and no source adaptation is required | After the review, if the URL is set |

`REVIEW_DEPENDENCY_BUMP_MODE` and `.codereview.yml` `review.dependency_bump_mode` select the mode. Operator env wins. Yaml cannot set `DEPENDENCY_BUMP_PUSH_URL` or `DEPENDENCY_BUMP_PUSH_SECRET`.

If `DEPENDENCY_BUMP_PUSH_URL` is set, CodeReview POSTs JSON to it after a version-bump review that called a model. Dry-run does not POST. The URL must be absolute http(s). Optional `DEPENDENCY_BUMP_PUSH_SECRET` is sent as `X-CodeReview-Token`. Redirects are not followed.

```json
{
  "repo": "group/proj",
  "project_id": 12,
  "merge_request_iid": 34,
  "merge_request_url": "https://gitlab.example.com/group/proj/-/merge_requests/34",
  "title": "Bump foo to v1.2.3",
  "author": "developer",
  "source_branch": "deps/foo-1.2.3",
  "target_branch": "main",
  "head_sha": "abc123…",
  "suggested_branch": "codereview/adapt-deps-mr-34",
  "dependency_files": ["go.mod", "go.sum"],
  "code_adaptation_required": true,
  "report": "Markdown checklist of code, config, and test work…",
  "summary": "Bumps foo to v1.2.3."
}
```

`suggested_branch` is a name only. CodeReview does not create the branch, push commits, or regenerate lockfiles.

## Optional GitLab extras

All default **off**. CodeReview never clicks Merge. GitLab still owns approval rules, CODEOWNERS, and prevent-author-approval.

| Setting | What it does |
|---|---|
| `GITLAB_COMMIT_STATUS_ENABLED` | Pending/success/failed commit status |
| `GITLAB_APPROVE_ON_CLEAN` | Approve when the verdict is Ready to merge; unapprove on Needs changes. Any clean MR. Needs a **personal** access token; project/group bots cannot approve. Fails closed if the token cannot approve |
| `GITLAB_APPROVE_DEPENDENCY_BUMPS` | Narrower: approve only dependency-only MRs that are Ready to merge |
| `REVIEW_DEPENDENCY_BUMP_MODE` | `notify`, `wait`, or `validate`. Default off |
| `DEPENDENCY_BUMP_PUSH_URL` | Operator webhook. After a version-bump review that called a model, POST repo, merge request, branches, files, and report. Not valid in project yaml |
| `DEPENDENCY_BUMP_PUSH_SECRET` | Optional shared secret sent as `X-CodeReview-Token`. Not valid in project yaml |
| `GITLAB_REQUEST_REVIEW` | Add the token user as a reviewer. Needs a personal access token |
| `TODO_MAX` | Max mention todos `codereview todos` processes in one run. Todos need a real user PAT |

## Debug CLI

`docker compose exec -it codereview-webhook codereview --project 12 --mr 34`

The CLI still accepts `CI_PROJECT_ID` / `CI_MERGE_REQUEST_IID` as aliases for `--project` / `--mr`. Without a project and MR it exits `1`. Exit `0` is success, skip, or dry-run; `1` is failure; `2` is blocking findings when `BLOCK_ON_FINDINGS` is on.
