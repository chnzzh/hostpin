GO ?= go
PNPM ?= pnpm
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
UPDATE_PUBLIC_KEY ?=
RELEASE_BASE ?= https://github.com/chnzzh/hostpin/releases/latest/download
LDFLAGS = -s -w -X github.com/chnzzh/hostpin/internal/buildinfo.Version=$(VERSION) -X github.com/chnzzh/hostpin/internal/buildinfo.Commit=$(COMMIT) -X github.com/chnzzh/hostpin/internal/buildinfo.ReleaseBase=$(RELEASE_BASE) -X github.com/chnzzh/hostpin/internal/updater.PublicKey=$(UPDATE_PUBLIC_KEY)

.PHONY: all web build test lint security release-check dev clean

all: build

web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build

build: web
	mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/hostpin-server ./cmd/hostpin-server
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/hostpin-agent ./cmd/hostpin-agent

test:
	$(GO) test ./...
	cd web && $(PNPM) test --run

lint:
	$(GO) vet ./...
	cd web && $(PNPM) typecheck

security:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
	cd web && $(PNPM) --registry=https://registry.npmjs.org audit --audit-level high

release-check: test lint build security
	bash -n scripts/*.sh
	tests/e2e/server_installer.sh
	python3 -m py_compile tests/e2e/*.py

dev:
	$(GO) run ./cmd/hostpin-server serve

clean:
	rm -rf bin dist coverage web/dist
