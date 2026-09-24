GO ?= go
OUTPUT ?= remote-mfi-for-xcertplay
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.Version=$(VERSION) \
	-X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.Commit=$(COMMIT) \
	-X github.com/cuckoohello/remote-mfi-for-xcertplay/internal/ver.BuildDate=$(BUILD_DATE)

.PHONY: all build fmt test race vet check clean

all: check build

build:
	mkdir -p "$(dir $(OUTPUT))"
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o "$(OUTPUT)" ./cmd/remote-mfi-for-xcertplay

fmt:
	@out=$$(gofmt -l ./cmd ./internal); \
	if [ -n "$$out" ]; then \
		echo "gofmt: needs formatting:"; echo "$$out"; exit 1; \
	fi

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: fmt test race vet

clean:
	rm -rf remote-mfi-for-xcertplay dist coverage.out
