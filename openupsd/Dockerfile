# syntax=docker/dockerfile:1
# Built locally by the Home Assistant Supervisor (it passes BUILD_FROM, BUILD_ARCH
# and BUILD_VERSION). The build runs natively on the target machine, so no
# GOARCH/GOARM handling is needed: Go builds for whatever the golang image is.
# Defaults keep a plain `docker build openupsd/` working too.
#
# The version number is maintained in config.yaml only. The Supervisor passes it
# as BUILD_VERSION; for a manual build it is read from config.yaml below.
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:3.20
ARG BUILD_VERSION=dev

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BUILD_VERSION
# Static, CGO-free: required because HA's userspace is musl.
# The -X path must be the full import path, otherwise it silently does nothing.
RUN set -eux; \
    v="${BUILD_VERSION}"; \
    if [ -z "$v" ] || [ "$v" = "dev" ]; then \
      if [ -f config.yaml ]; then \
        v="$(awk '/^version:/{print $2; exit}' config.yaml | tr -d '"')"; \
      fi; \
    fi; \
    if [ -z "$v" ]; then v="dev"; fi; \
    export CGO_ENABLED=0 GOOS=linux; \
    ld="-s -w -X github.com/herrfrei/openupsd/internal/core.DriverVersion=${v}"; \
    go build -trimpath -ldflags="$ld" -o /out/openupsd ./cmd/openupsd; \
    go build -trimpath -ldflags="$ld" -o /out/hidprobe ./cmd/hidprobe

FROM ${BUILD_FROM}
ARG BUILD_ARCH BUILD_VERSION=dev
LABEL io.hass.type="addon" \
      io.hass.arch="${BUILD_ARCH}" \
      io.hass.version="${BUILD_VERSION}" \
      io.hass.name="OpenUPSd"
COPY --from=build /out/openupsd /usr/local/bin/openupsd
COPY --from=build /out/hidprobe /usr/local/bin/hidprobe
COPY --chmod=755 run.sh /usr/local/bin/run.sh
CMD ["/usr/local/bin/run.sh"]