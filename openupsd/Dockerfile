# syntax=docker/dockerfile:1
# Built locally by the Home Assistant Supervisor (it passes BUILD_FROM, BUILD_ARCH
# and BUILD_VERSION). The build runs natively on the target machine, so no
# GOARCH/GOARM handling is needed: Go builds for whatever the golang image is.
# Default keeps a plain `docker build openupsd/` working too.
ARG BUILD_FROM=ghcr.io/home-assistant/amd64-base:3.20

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, CGO-free: required because HA's userspace is musl.
RUN set -eux; \
    export CGO_ENABLED=0 GOOS=linux; \
    go build -trimpath -ldflags='-s -w' -o /out/openupsd ./cmd/openupsd; \
    go build -trimpath -ldflags='-s -w' -o /out/hidprobe ./cmd/hidprobe

FROM ${BUILD_FROM}
ARG BUILD_ARCH BUILD_VERSION
LABEL io.hass.type="addon" \
      io.hass.arch="${BUILD_ARCH}" \
      io.hass.version="${BUILD_VERSION}" \
      io.hass.name="OpenUPSd"
COPY --from=build /out/openupsd /usr/local/bin/openupsd
COPY --from=build /out/hidprobe /usr/local/bin/hidprobe
COPY --chmod=755 run.sh /usr/local/bin/run.sh
CMD ["/usr/local/bin/run.sh"]
