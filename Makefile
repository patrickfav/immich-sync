APP := immich-sync
BINARY := bin/$(APP)
GO ?= go

.PHONY: help build run test vet fmt fmt-check check clean

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## / {printf "%-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the binary into bin/
	@mkdir -p bin
	$(GO) build -o $(BINARY) .

run: ## Run the program; pass CLI flags with ARGS="..."
	$(GO) run . $(ARGS)

test: ## Run unit tests
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format Go source files
	gofmt -w *.go

fmt-check: ## Check Go source formatting without changing files
	test -z "$$(gofmt -l *.go)"

check: fmt-check test vet build ## Run all local verification checks

clean: ## Remove build artifacts
	rm -rf bin
