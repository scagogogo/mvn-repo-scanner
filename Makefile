.PHONY: build test test-race test-cover integration-test clean install lint run-test

BINARY=mvn-repo-scanner
GO=go
MAIN=./cmd

build:
	$(GO) build -o $(BINARY) $(MAIN)

test:
	$(GO) test ./... -v -count=1

test-race:
	$(GO) test -race ./... -v -count=1

test-cover:
	$(GO) test ./... -cover -coverprofile=coverage.out
	$(GO) tool cover -html=coverage.out -o coverage.html

integration-test:
	$(GO) test -tags integration -v -timeout 10m ./tests/integration/

clean:
	rm -f $(BINARY) coverage.out coverage.html

install: build
	$(GO) install $(MAIN)

lint:
	golangci-lint run ./...

run-test: build
	./$(BINARY) scan --repo https://repo.maven.apache.org/maven2 --group org.apache.commons.commons-collections4 --concurrency 3 --qps 5 --verbose --output json --output-file /tmp/mvn-scan-test-report.json
	@echo "--- Report ---"
	@cat /tmp/mvn-scan-test-report.json | head -50
