# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.24

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

ARG APP=server
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

RUN apk add --no-cache ca-certificates git

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN test "$APP" = "server" -o "$APP" = "client"
RUN CGO_ENABLED=0 \
    GOOS="${TARGETOS:-linux}" \
    GOARCH="${TARGETARCH:-amd64}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/acmedeliver "./cmd/${APP}"

FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S acmedeliver \
    && adduser -S -D -H -G acmedeliver acmedeliver \
    && mkdir -p /app /data \
    && chown -R acmedeliver:acmedeliver /app /data

WORKDIR /app

COPY --from=builder /out/acmedeliver /usr/local/bin/acmedeliver

ENV ACMEDELIVER_BIND=0.0.0.0 \
    ACMEDELIVER_PORT=9090 \
    ACMEDELIVER_BASE_DIR=/data

EXPOSE 9090 9443
VOLUME ["/data"]

USER acmedeliver

ENTRYPOINT ["/usr/local/bin/acmedeliver"]
