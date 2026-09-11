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

.PHONY: build test test-frontend lint lint-ci verify docker-build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

test:
	go test -race -coverprofile=coverage.out ./...

# Frontend assets use only the Node standard library, so no install step is
# needed. These cover the cached formatters and export chunking that the
# dashboard performance work depends on.
test-frontend:
	node internal/dashboard/frontend-check.mjs internal/dashboard

# Fast local check. CI runs the full golangci-lint suite; use `make lint-ci` to
# reproduce it exactly before pushing.
lint:
	go vet ./...

lint-ci:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

verify: lint lint-ci test test-frontend build

docker-build:
	docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(APP):local .
