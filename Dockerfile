FROM golang:1.23-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/codex-chat-api ./cmd/codex-chat-api

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata nodejs npm \
	&& npm install -g @anthropic-ai/claude-code@2.1.200

WORKDIR /app

COPY --from=build /out/codex-chat-api /usr/local/bin/codex-chat-api
COPY codex_profile.json codex_scaffold.json ./

EXPOSE 8088

ENTRYPOINT ["/usr/local/bin/codex-chat-api"]
