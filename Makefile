# Vela build targets. Run "make help" for a summary.

MODULE   := go.xchunk.org/vela
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)
GOFLAGS_BUILD := -trimpath -ldflags "$(LDFLAGS)"

# The linter version is pinned and run through "go run", so every machine
# and CI use the same release, built with the current Go toolchain.
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

IMAGE    ?= xchunk/vela
DEV_ADDR ?= 127.0.0.1:8080
DEV_DATA ?= .dev-data

export CGO_ENABLED := 0

.PHONY: help build build-cli build-server install test test-e2e lint fmt tidy docker dev clean

help: ## Show this help
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: build-cli build-server ## Build vela and velad into ./bin

build-cli: ## Build the vela CLI into ./bin
	go build $(GOFLAGS_BUILD) -o bin/vela ./cmd/vela

build-server: ## Build the velad server into ./bin
	go build $(GOFLAGS_BUILD) -o bin/velad ./cmd/velad

install: ## Install vela into $(go env GOBIN) or $(go env GOPATH)/bin
	go install $(GOFLAGS_BUILD) ./cmd/vela

# -race needs cgo; it is the only place where cgo is enabled, and it does
# not affect the binaries.
test: ## Run all tests with the race detector
	CGO_ENABLED=1 go test -race -count=1 ./...

test-e2e: ## Check accel delivery through a real nginx (needs Docker)
	go test -tags e2e -count=1 -v ./test/e2e/...

lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

fmt: ## Format code with gofmt and goimports
	$(GOLANGCI_LINT) fmt ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

docker: ## Build the Docker image locally
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):dev .

# dev runs velad without nginx (direct delivery) on $(DEV_ADDR) with its data
# in $(DEV_DATA), and prints a fresh admin key for "vela login".
dev: build ## Run velad locally in direct mode and print an admin key
	@mkdir -p $(DEV_DATA)
	@key=$$(VELA_DATA_DIR=$(DEV_DATA) VELA_LOG_FORMAT=text ./bin/velad key create --name dev-$$(date +%s) --scope admin 2>/dev/null) && \
	echo "" && \
	echo "  Admin key: $$key" && \
	echo "  Log in:    echo '$$key' | ./bin/vela login --url http://$(DEV_ADDR) --profile dev" && \
	echo "  Index:     http://$(DEV_ADDR)/" && \
	echo ""
	VELA_DATA_DIR=$(DEV_DATA) \
	VELA_PUBLIC_URL=http://$(DEV_ADDR) \
	VELA_LISTEN=$(DEV_ADDR) \
	VELA_ACCEL_PREFIX= \
	VELA_LOG_FORMAT=text \
	VELA_SITE_TITLE="vela (dev)" \
	./bin/velad serve

clean: ## Remove build output and development data
	rm -rf bin dist $(DEV_DATA)
