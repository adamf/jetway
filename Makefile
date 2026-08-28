# Jetway — GDS and airline messaging gateway.

GO      ?= go
BIN     ?= bin
PKGS    := ./...
DSN     ?= postgres://jetway@127.0.0.1:5432/jetway?sslmode=disable

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "};{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build every binary into ./bin
	@mkdir -p $(BIN)
	$(GO) build -o $(BIN)/jetwayd    ./cmd/jetwayd
	$(GO) build -o $(BIN)/jetwayctl  ./cmd/jetwayctl
	$(GO) build -o $(BIN)/carriersim ./cmd/carriersim

.PHONY: test
test: ## Run the test suite
	$(GO) test $(PKGS)

.PHONY: test-pg
test-pg: ## Run the suite including the Postgres store conformance tests
	JETWAY_TEST_DSN="$(DSN)" $(GO) test $(PKGS)

.PHONY: race
race: ## Run the suite under the race detector
	$(GO) test -race $(PKGS)

.PHONY: cover
cover: ## Report test coverage
	$(GO) test -coverprofile=coverage.out $(PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: fuzz
fuzz: ## Fuzz the EDIFACT codec for 60s (override with FUZZTIME=)
	$(GO) test ./pkg/edifact/ -run=FuzzRoundTrip -fuzz=FuzzRoundTrip -fuzztime=$${FUZZTIME:-60s}

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKGS)

.PHONY: fmt
fmt: ## Format all Go source
	gofmt -l -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: demo
demo: ## Run the gateway with the simulated carrier fleet and open the console
	@echo "console: http://127.0.0.1:8080"
	$(GO) run ./cmd/jetwayd

.PHONY: schema
schema: ## Print the database schema
	@$(GO) run ./cmd/jetwayctl schema 2>/dev/null || \
	  cat internal/store/migrations/*.sql

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN) coverage.out
