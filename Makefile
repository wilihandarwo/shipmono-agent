# ShipMono agent — build, test, and reproducible release artifacts.
#
# `make release` produces exactly the files the control plane's public/install.sh
# downloads and checksum-verifies:
#   dist/shipmono-agent-linux-amd64
#   dist/shipmono-agent-linux-arm64
#   dist/checksums.txt

PKG        := github.com/wilihandarwo/shipmono-agent
VERSION    ?= dev
DIST       := dist
LDFLAGS    := -s -w -X $(PKG)/internal/version.Version=$(VERSION)
# Reproducible: -trimpath strips local paths, CGO off for a static binary,
# -buildvcs=false drops VCS stamping that would vary the output.
GOFLAGS    := -trimpath -buildvcs=false
BUILDENV   := CGO_ENABLED=0

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the agent for the host platform into dist/
	$(BUILDENV) go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/shipmono-agent ./cmd/shipmono-agent

.PHONY: build-linux
build-linux: ## Cross-compile static linux/amd64 and linux/arm64 binaries
	$(BUILDENV) GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/shipmono-agent-linux-amd64 ./cmd/shipmono-agent
	$(BUILDENV) GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/shipmono-agent-linux-arm64 ./cmd/shipmono-agent

.PHONY: release
release: build-linux ## Build both arches and write checksums.txt
	cd $(DIST) && shasum -a 256 shipmono-agent-linux-amd64 shipmono-agent-linux-arm64 > checksums.txt
	@echo "Release artifacts in $(DIST)/ (version $(VERSION)):"
	@cat $(DIST)/checksums.txt

.PHONY: verify-reproducible
verify-reproducible: ## Build twice and confirm byte-identical linux binaries
	$(MAKE) build-linux DIST=dist-a
	$(MAKE) build-linux DIST=dist-b
	@diff dist-a/shipmono-agent-linux-amd64 dist-b/shipmono-agent-linux-amd64 && \
	 diff dist-a/shipmono-agent-linux-arm64 dist-b/shipmono-agent-linux-arm64 && \
	 echo "Reproducible: byte-identical." || (echo "NOT reproducible" && exit 1)
	@rm -rf dist-a dist-b

.PHONY: test
test: ## Run the test suite with the race detector
	go test -race -count=1 ./...

.PHONY: vet
vet: ## go vet (host + linux build tags)
	go vet ./...
	GOOS=linux go vet ./...

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

.PHONY: staticcheck
staticcheck: ## Run staticcheck for host + linux build tags (installed on demand)
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@2025.1.1
	staticcheck ./...
	GOOS=linux staticcheck ./...

.PHONY: lint
lint: fmt-check vet staticcheck ## All static checks

.PHONY: ci
ci: lint test build-linux ## What CI runs

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(DIST) dist-a dist-b

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
