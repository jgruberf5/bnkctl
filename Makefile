.PHONY: build test vet tidy run clean

VERSION ?= dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BIN     := bin/bnkctl
PKG     := github.com/jgruberf5/bnkctl

LDFLAGS := -s -w \
	-X $(PKG)/internal/cli.Version=$(VERSION) \
	-X $(PKG)/internal/cli.Commit=$(COMMIT) \
	-X $(PKG)/internal/cli.BuildDate=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/bnkctl

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

run: build
	$(BIN) --help

clean:
	rm -rf bin/
