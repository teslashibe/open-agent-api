FROM golang:1.26.10-alpine AS build

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

FROM node:22.23.4-alpine

# Claude Code CLI for the claude provider. Pinned: the provider replays
# history through the CLI's session transcript format, which is not a public
# contract, so bump deliberately and re-run the claude live checks.
ARG CLAUDE_CODE_VERSION=2.1.286
RUN apk upgrade --no-cache \
	&& apk add --no-cache ca-certificates tzdata \
	&& npm install -g npm@12.2.1 \
	&& npm install -g --ignore-scripts @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
	&& node /usr/local/lib/node_modules/@anthropic-ai/claude-code/install.cjs \
	&& claude --version \
	&& mkdir -p /var/lib/open-agent-api/telemetry /tmp/open-agent-api-claude \
	&& chown -R 1000:1000 /var/lib/open-agent-api /tmp/open-agent-api-claude

WORKDIR /app

COPY --from=build /out/open-agent-api /usr/local/bin/open-agent-api
COPY --chmod=0644 codex_profile.json codex_scaffold.json ./

EXPOSE 8088

ENTRYPOINT ["/usr/local/bin/open-agent-api"]
