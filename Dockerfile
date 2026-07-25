FROM golang:1.24-alpine AS build

WORKDIR /src

ARG SOURCE_REVISION=unknown
ARG IMAGE_VERSION=development

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
	-ldflags "-X github.com/teslashibe/codex-chat-api/internal/buildinfo.SourceRevision=${SOURCE_REVISION} -X github.com/teslashibe/codex-chat-api/internal/buildinfo.ImageVersion=${IMAGE_VERSION}" \
	-o /out/codex-chat-api ./cmd/codex-chat-api

FROM alpine:3.20

ARG SOURCE_REVISION=unknown
ARG IMAGE_VERSION=development

LABEL org.opencontainers.image.revision="${SOURCE_REVISION}" \
	org.opencontainers.image.version="${IMAGE_VERSION}" \
	io.teslashibe.report-studio.contract="report-studio.structured-inference.v1"

RUN apk add --no-cache ca-certificates tzdata nodejs npm \
	&& npm install -g @anthropic-ai/claude-code@2.1.200

WORKDIR /app

COPY --from=build /out/codex-chat-api /usr/local/bin/codex-chat-api
COPY codex_profile.json codex_scaffold.json ./

EXPOSE 8088

ENTRYPOINT ["/usr/local/bin/codex-chat-api"]
