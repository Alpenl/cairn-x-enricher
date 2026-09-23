APP := cairn-x-enricher
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOPROXY ?= https://proxy.golang.org,direct
GOLANGCI_LINT_VERSION ?= v2.13.2
LDFLAGS := -s -w \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Version=$(VERSION) \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Date=$(BUILD_DATE)

.PHONY: build test test-frontend test-browser test-image-browser lint lint-ci verify docker-build test-ablation ablation ablation-architecture

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

test:
	go test -race -coverprofile=coverage.out ./...

# Frontend assets use only the Node standard library, so no install step is
# needed. These cover the cached formatters and export chunking that the
# dashboard performance work depends on.
# Real-browser acceptance for the multidimensional curation UI. It uses the
# system Chrome and a bundled mock Worker, so it makes no paid or network call.
# Enable with CHROME_PATH when Chrome is not on the default path.
test-browser:
	CHROME_PATH=$${CHROME_PATH:-$$(command -v google-chrome || command -v chromium || command -v chromium-browser)} node tests/browser/run.mjs

# Real Chrome cache + actual Go proxy/client against a legacy HTTP fixture.
# No route interception/cache disabling; no model or external source calls.
test-image-browser:
	CAIRN_IMAGE_BROWSER=1 CHROME_PATH=$${CHROME_PATH:-$$(command -v google-chrome || command -v chromium || command -v chromium-browser)} go test ./internal/dashboard -run TestBrowserPrivateImageCache -count=1 -v

test-frontend:
	node internal/dashboard/frontend-check.mjs internal/dashboard

# Fast local check. CI runs the full golangci-lint suite; use `make lint-ci` to
# reproduce it exactly before pushing.
lint:
	go vet ./...

lint-ci:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

verify: lint lint-ci test test-frontend build

# Offline replay of the recorded ablation outcomes. It makes no model calls, so
# it is safe to run on every change and is what pins the published conclusions.
test-ablation:
	go test ./experiments/... ./internal/evaluation/...

# Offline behavior mutations in a disposable working-tree copy. No model calls.
ablation-architecture:
	python3 experiments/architecture/run.py

# Live ablation run against the configured endpoint. This costs real model
# calls and takes tens of minutes, so it is never part of verify or CI.
ablation: test-ablation
	go run ./experiments/verifyurls -env .env
	go run ./experiments/ablation/main -env .env -concurrency 2 \
		-out experiments/results/ablation.json -timeout 150m
	go run ./experiments/rescore -in experiments/results/ablation.jsonl \
		-out experiments/results/ablation.json
	go run ./experiments/ablation-report experiments/results > experiments/results/ablation-report.md

docker-build:
	docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(APP):local .
