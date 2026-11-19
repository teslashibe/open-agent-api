FROM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS build

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

FROM node:22.23.4-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914

# Claude Code CLI for the claude provider. Pinned: the provider replays
# history through the CLI's session transcript format, which is not a public
# contract, so bump deliberately and re-run the claude live checks.
ARG CLAUDE_CODE_VERSION=2.1.286
# npm 12.2.1 bundles undici 6.28.0 (CVE-2026-109383, CVE-2026-109869,
# CVE-2026-87018, fixed in 6.29.1), so that one package is replaced by the
# 6.29.1 release, as smore-agents' Dockerfile does.
ARG NPM_UNDICI_VERSION=6.29.1
RUN apk upgrade --no-cache \
	&& apk add --no-cache ca-certificates tzdata \
	&& npm install -g npm@12.2.1 \
	&& cd /usr/local/lib/node_modules/npm/node_modules \
	&& npm pack --silent "undici@$NPM_UNDICI_VERSION" > /tmp/undici-pack \
	&& rm -rf undici && mkdir undici \
	&& tar -xzf "$(cat /tmp/undici-pack)" -C undici --strip-components=1 \
	&& rm -f "$(cat /tmp/undici-pack)" /tmp/undici-pack \
	&& test "$(node -p 'require("./undici/package.json").version')" = "$NPM_UNDICI_VERSION" \
	&& npm cache clean --force \
	&& cd / \
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
