# CloudAC build and check targets.
#
# This exists so the build commands are documented by being executable, and so
# CI calls the same thing a developer does rather than duplicating command lines
# in YAML.

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS := -trimpath

.DEFAULT_GOAL := help
.PHONY: help build build-go build-java test test-go fuzz race check bench clean

help: ## Show this help.
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: build-go build-java ## Build both components.

build-go: ## Build the ComputationServer.
	cd ComputationServer && go build $(GOFLAGS) \
		-ldflags "-X main.version=$(VERSION)" -o ../bin/cserver .

build-java: ## Build the plugin jar.
	cd ACTransmitter && mvn -B package

test: test-go ## Run the test suite.

test-go: ## Run Go tests.
	cd ComputationServer && go test ./...

race: ## Run Go tests under the race detector.
	cd ComputationServer && go test -race ./...

fuzz: ## Fuzz the wire parser for 60 seconds.
	cd ComputationServer && go test -run '^$$' -fuzz FuzzParsePacket -fuzztime 60s ./sys/

bench: ## Measure the transmitter hot path. This is the project's own claim.
	javac --release 8 -d bin/bench bench/SenderBenchmark.java
	java -cp bin/bench SenderBenchmark

check: ## Static checks. None of these catch the defects tests catch.
	cd ComputationServer && gofmt -l . && go vet ./...
	@command -v govulncheck >/dev/null 2>&1 \
		&& (cd ComputationServer && govulncheck ./...) \
		|| echo "govulncheck not installed, skipping (go install golang.org/x/vuln/cmd/govulncheck@latest)"

clean: ## Remove build output.
	rm -rf bin ACTransmitter/target
