# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine3.20 AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN apk add --no-cache \
    build-base \
    libusb-dev=1.0.27-r0 \
    pkgconfig

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w \
      -X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.Version=${VERSION} \
      -X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.Commit=${COMMIT} \
      -X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.BuildDate=${BUILD_DATE}" \
    -o /out/remote-mfi-for-xcertplay \
    ./cmd/remote-mfi-for-xcertplay

FROM alpine:3.20

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="remote-mfi-for-xcertplay" \
      org.opencontainers.image.description="Remote HTTP adapter for an MFi authentication coprocessor over CH341 USB-I2C" \
      org.opencontainers.image.source="https://github.com/cuckoohello/remote-mfi-for-xcertplay" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$COMMIT \
      org.opencontainers.image.created=$BUILD_DATE \
      org.opencontainers.image.licenses="GPL-3.0-only"

RUN apk add --no-cache \
      ca-certificates \
      libusb=1.0.27-r0 \
      tzdata \
    && addgroup -S -g 10001 mfi \
    && adduser -S -D -H -u 10001 -G mfi -s /sbin/nologin mfi

COPY --from=build /out/remote-mfi-for-xcertplay /usr/local/bin/remote-mfi-for-xcertplay

USER mfi
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O- http://127.0.0.1:8080/healthz | grep -q '"ok":true' || exit 1

ENTRYPOINT ["/usr/local/bin/remote-mfi-for-xcertplay"]
