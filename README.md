# CodeReview

Self-hosted reviewer for GitLab merge requests. GitLab sends webhook events; one container reviews and posts notes.

Pin the image by digest in production. `:latest` is not a pin.

## Install

The image starts `codereview-webhook`. It has no API keys inside.

**Operator** (once, on the host that runs the container)

- `GITLAB_TOKEN` — group access token, `api` scope
- `AI_PROVIDER` — CSV of providers to offer; first is the default
- One bind mount `./data:/data` — `ai-keys.json`, state, and Grok login. Sample keys shape: [examples/ai-keys.json](examples/ai-keys.json)
- `WEBHOOK_SECRET` and/or `WEBHOOK_SIGNING_TOKEN` — GitLab secret token and/or signing token
- Invite `@codereview` as Developer on the group

```sh
docker compose up -d
```

In GitLab, add a **group webhook** (Merge requests + Comments) to `http://127.0.0.1:8090/gitlab-webhook`. Allow the URL under Admin → Settings → Network → Outbound requests. System Hooks do not deliver comments.

**Reviewed project** (optional, from the MR head SHA; not secrets)

- [`.codereview.yml`](examples/codereview.yml) — `provider` from `AI_PROVIDER`, `model`, mode, ignore globs
- [`.codereviewignore`](examples/.codereviewignore) — extra ignore globs. Missing file is a no-op

What each option means: [Configuration](docs/CONFIGURATION.md).

Debug: `docker compose exec -it codereview-webhook codereview --project 12 --mr 34`

Health: `GET http://127.0.0.1:8090/health`

## Behaviour

The walkthrough starts with **Ready to merge** or **Needs changes**. Inline notes attach to added lines. An incremental run that only covers part of the diff keeps **Needs changes** while earlier finding threads remain open.

`@codereview` with no command prints the list. Useful commands: `skip`, `review`, `full-review`, `ask`, `resolve`, `help`. Title or label tokens such as `skip-codereview` disable the bot (silent by default).

Optional GitLab Approve, commit status, and reviewer assignment are off unless you turn them on. CodeReview does not merge.

## Version bumps

When a merge request changes package manifests or lockfiles, CodeReview can treat it as a version bump. Default off.

| Mode | What CodeReview does |
|---|---|
| `notify` | Notes that new versions were found. Does not call a model |
| `wait` | Waits until someone comments `@codereview review` |
| `validate` | Reviews the diff. Approves only when nothing but manifests/lockfiles changed and no source adaptation is required |

Set `REVIEW_DEPENDENCY_BUMP_MODE` on the operator, or `review.dependency_bump_mode` in `.codereview.yml`. Operator env wins. Project yaml cannot set tokens or webhook URLs.

`DEPENDENCY_BUMP_PUSH_URL` is operator-only. When it is set, a version-bump review that called a model POSTs the repo, merge request, branches, changed files, and adaptation report to that URL. Dry-run does not POST. CodeReview does not clone, commit, or merge.

Full settings: [Configuration](docs/CONFIGURATION.md#version-bumps).
