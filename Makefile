BIN_DIR := bin

.PHONY: build run-gateway run-testupstream test test-race test-integration vet lint tidy fmt clean

build:
	go build -o $(BIN_DIR)/gatekeeper ./cmd/gatekeeper
	go build -o $(BIN_DIR)/testupstream ./cmd/testupstream

run-gateway: build
	$(BIN_DIR)/gatekeeper --config=configs/gatekeeper.example.yaml

run-testupstream: build
	$(BIN_DIR)/testupstream

test:
	go test ./...

test-race:
	go test -race ./...

# Needs a Redis reachable at REDIS_ADDR (default localhost:6379), e.g.:
#   docker run -d -p 6379:6379 redis:7-alpine
test-integration:
	go test -tags=integration -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

fmt:
	gofmt -l -w .

clean:
	rm -rf $(BIN_DIR)
