APP := cairn-x-enricher
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GOPROXY ?= https://proxy.golang.org,direct
LDFLAGS := -s -w \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Version=$(VERSION) \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/Alpenl/cairn-x-enricher/internal/buildinfo.Date=$(BUILD_DATE)

.PHONY: build test lint verify docker-build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

test:
	go test -race -coverprofile=coverage.out ./...

lint:
	go vet ./...

verify: lint test build

docker-build:
	docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(APP):local .
