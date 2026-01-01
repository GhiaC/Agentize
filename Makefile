.PHONY: build run test clean

# Build the agentize server
build:
	go build -o bin/agentize cmd/agentize/main.go

# Run the server (requires environment variables)
run:
	go run cmd/agentize/main.go

# Run with HTTP enabled
run-server:
	AGENTIZE_HTTP_ENABLED=true \
	AGENTIZE_FEATURE_HTTP=true \
	AGENTIZE_KNOWLEDGE_PATH=./knowledge \
	go run cmd/agentize/main.go

# Run tests
test:
	go test ./...

# Run tests with verbose output
test-verbose:
	go test -v ./...

# Clean build artifacts
clean:
	rm -rf bin/

# Install dependencies
deps:
	go mod tidy
	go get github.com/go-echarts/go-echarts/v2

