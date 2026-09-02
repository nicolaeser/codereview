# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.27
ARG DEBIAN_VERSION=trixie

FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -tags=netgo,osusergo -ldflags="-s -w" -o /out/codereview ./cmd/codereview \
    && CGO_ENABLED=0 go build -trimpath -tags=netgo,osusergo -ldflags="-s -w" -o /out/codereview-webhook ./cmd/codereview-webhook

FROM debian:${DEBIAN_VERSION}-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /app /data \
    && chown -R 10001:10001 /app /data
WORKDIR /app
COPY --from=build /out/codereview /usr/local/bin/codereview
COPY --from=build /out/codereview-webhook /usr/local/bin/codereview-webhook
COPY --chown=10001:10001 INSTRUCTION.md /app/INSTRUCTION.md
ENV INSTRUCTION_PATH=/app/INSTRUCTION.md \
    INSTRUCTION_ADDITIONAL_PATH=/app/INSTRUCTION-ADDITIONAL.md \
    STATE_PATH=/data/state.json \
    AI_KEYS_FILE=/data/ai-keys.json \
    CODEREVIEW_AUTH_PATH=/data/auth.json
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/codereview-webhook"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["codereview-webhook", "health"]
