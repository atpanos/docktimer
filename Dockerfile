# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/docktimer ./cmd/server

# ---- runtime stage --------------------------------------------------------
FROM alpine:3.24

RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S -g 10001 docktimer \
 && adduser -S -D -H -u 10001 -G docktimer docktimer \
 && mkdir -p /data \
 && chown 10001:10001 /data

COPY --from=build /out/docktimer /usr/local/bin/docktimer

USER 10001:10001
WORKDIR /data

ENV APP_PORT=8080 \
    DATABASE_PATH=/data/docktimer.db \
    DEFAULT_TIMEZONE=Europe/Berlin

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${APP_PORT}/healthz" >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/docktimer"]
