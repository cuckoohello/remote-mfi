GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/cuckoohello/remote-mfi/internal/ver.Version=$(VERSION) \
	-X github.com/cuckoohello/remote-mfi/internal/ver.Commit=$(COMMIT) \
	-X github.com/cuckoohello/remote-mfi/internal/ver.BuildDate=$(BUILD_DATE)

.PHONY: all build test race vet check clean

all: check build

build:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o remote-mfi ./cmd/remote-mfi

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: test race vet

clean:
	rm -rf remote-mfi dist coverage.out
