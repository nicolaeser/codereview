---
type: instruction
description: Defines mandatory delivery and packaging rules for configuration, containers, optional state cache, GitHub Actions, and image release.
scope: repository
---

# Operations and delivery

## Scope and activation

Load this instruction for changes to runtime configuration, `Dockerfile`, Compose files, optional state cache, GitHub Actions, Dependabot, release workflows, or consumer install examples.

## Mandatory rules

- `internal/config/config.go` is authoritative for environment values, defaults, normalization, and validation.
- Real GitLab tokens and AI credentials must stay outside tracked files. Compose placeholders must never become real secrets. Operator keys are bind-mounted (`AI_KEYS_FILE`, writable) or set in the operator environment (`AI_KEYS_JSON`). When `AI_KEYS_FILE` is set, the process rewrites that file to match the enabled provider list and must not log its contents.
- The product process is `codereview-webhook`. The image `ENTRYPOINT` must start that binary. The one-shot `codereview` CLI remains for debug and must not listen.
- GitHub Actions in this repository must test on pull request and push; image publish and GitHub release must run only on push to `development` or `main` respectively (`.github/workflows/development.yml`, `.github/workflows/main.yml`). A development job must not publish GHCR tag `latest` or a non-prerelease GitHub Release. A main job must not publish GHCR tags `dev` or `development`.
- `INSTRUCTION.md` is required runtime product content copied into the image. It is not `.ai/` development context.
- The image runs as UID/GID `10001`.
- Release changes must retain test-before-publish ordering, multi-arch images, immutable tags, provenance, and SBOM generation where already present.
- Consumer install examples must show `GITLAB_TOKEN`, `AI_PROVIDER`, `AI_KEYS_FILE`, `WEBHOOK_SECRET`, and a group webhook with Merge requests + Comments.

## Architecture notes

- GitHub Actions in this repository validates Go code and publishes container images; it does not run CodeReview against live merge requests by default.
- Dependabot (`.github/dependabot.yml`) updates Go modules, Docker bases, Compose helpers, and Actions on this GitHub repository.
- `STATE_PATH` on a volume enables incremental reviews across process restarts.
- Optional GitLab commit statuses are merge-gate hooks; the webhook does not fail a GitLab CI job.

## Commands

```sh
test -z "$(gofmt -l cmd internal)"
go vet ./...
go test -race -count=1 ./...
go build -trimpath -o /tmp/codereview ./cmd/codereview
go build -trimpath -o /tmp/codereview-webhook ./cmd/codereview-webhook
```

When Compose files change:

```sh
docker compose -f docker-compose.yml config -q
docker compose -f docker-compose.dev.yml config -q
```
