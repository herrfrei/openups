# Version comes from config.yaml, the single place it is maintained.
VERSION := $(shell awk '/^version:/{print $$2; exit}' config.yaml | tr -d '"')
LDFLAGS := -s -w -X github.com/herrfrei/openupsd/internal/core.DriverVersion=$(VERSION)
GOFLAGS := CGO_ENABLED=0 GOOS=linux

.PHONY: all build check clean version

all: build

version:
	@echo $(VERSION)

build:
	$(GOFLAGS) go build -trimpath -ldflags="$(LDFLAGS)" -o openupsd ./cmd/openupsd
	$(GOFLAGS) go build -trimpath -ldflags="$(LDFLAGS)" -o hidprobe ./cmd/hidprobe

check:
	gofmt -l . && go vet ./... && go test ./...

clean:
	rm -f openupsd hidprobe