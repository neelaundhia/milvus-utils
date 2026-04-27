.PHONY: help test tidy build mod-graph go run envs clean

help:
	@echo "Available targets:"
	@echo "  make test         - Run tests"
	@echo "  make tidy         - Run go mod tidy"
	@echo "  make build        - Build binary"
	@echo "  make mod-graph    - Show dependency graph (go mod graph)"
	@echo "  make go CMD=...   - Run any 'go' command (e.g., make go CMD='get github.com/foo/bar')"
	@echo "  make run CMD=...  - Run a command (e.g., make run CMD='snapshot create')"
	@echo "  make envs         - Run the envs command"
	@echo "  make clean        - Remove build artifacts"

test:
	go test ./...

tidy:
	go mod tidy

build:
	go build -o /tmp/milvus-utils main.go

mod-graph:
	go mod graph

go:
	go $(CMD)

run:
	go run main.go $(CMD)

envs:
	go run main.go envs

clean:
	rm -f /tmp/milvus-utils
	@echo "Removed build artifacts"
