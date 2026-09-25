BIN_DIR := bin

.PHONY: build run-gateway run-testupstream test test-race vet lint tidy fmt clean

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
