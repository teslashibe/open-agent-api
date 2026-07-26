FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/open-agent-api ./cmd/open-agent-api

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata nodejs npm \
	&& npm install -g @anthropic-ai/claude-code@2.1.200

WORKDIR /app

COPY --from=build /out/open-agent-api /usr/local/bin/open-agent-api
COPY codex_profile.json codex_scaffold.json ./

EXPOSE 8088

ENTRYPOINT ["/usr/local/bin/open-agent-api"]
