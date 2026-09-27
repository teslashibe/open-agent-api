FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build provenance. Pass these through --build-arg (docker compose sets them
# from the environment) so a running gateway can be tied back to the exact
# source it was built from; unset values fall back to the Go toolchain's VCS
# stamps and finally to "devel"/"unknown".
ARG BUILD_VERSION=""
ARG BUILD_COMMIT=""
ARG BUILD_DATE=""
RUN CGO_ENABLED=0 GOOS=linux go build \
	-ldflags "-X github.com/teslashibe/open-agent-api/internal/buildinfo.Version=${BUILD_VERSION} -X github.com/teslashibe/open-agent-api/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/teslashibe/open-agent-api/internal/buildinfo.BuildDate=${BUILD_DATE}" \
	-o /out/open-agent-api ./cmd/open-agent-api

FROM alpine:3.20

# Claude Code CLI for the claude provider. Pinned: the provider replays
# history through the CLI's session transcript format, which is not a public
# contract, so bump deliberately and re-run the claude live checks.
ARG CLAUDE_CODE_VERSION=2.1.283
RUN apk add --no-cache ca-certificates tzdata nodejs npm \
	&& npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
	&& mkdir -p /var/lib/open-agent-api/telemetry /tmp/open-agent-api-claude \
	&& chown -R 1000:1000 /var/lib/open-agent-api /tmp/open-agent-api-claude

WORKDIR /app

COPY --from=build /out/open-agent-api /usr/local/bin/open-agent-api
COPY --chmod=0644 codex_profile.json codex_scaffold.json ./

EXPOSE 8088

ENTRYPOINT ["/usr/local/bin/open-agent-api"]
