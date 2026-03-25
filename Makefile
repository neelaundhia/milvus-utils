.PHONY: Help test, tidy, build, mod-graph, run, clean

# Container configuration
CONTAINER_IMAGE := docker.io/library/golang:1.25
PODMAN_FLAGS := --rm -it -v "$$(pwd)":/app:z -v go-mod-cache:/go/pkg/mod:z -v go-build-cache:/root/.cache/go-build:z -w /app

help:
	@echo "Available targets:"
	@echo "  make test         - Run tests in container"
	@echo "  make tidy         - Run go mod tidy in container"
	@echo "  make build        - Build binary in container"
	@echo "  make mod-graph    - Show dependency graph (go mod graph)"
	@echo "  make go CMD=...   - Run any 'go' command (e.g., make go CMD='get github.com/foo/bar')"
	@echo "  make run CMD=...  - Run a command (e.g., make run CMD='snapshot create')"
	@echo "  make envs         - Run the envs command"
	@echo "  make clean        - Remove Go module and build caches"

test:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go test ./...

tidy:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go mod tidy

build:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go build -o /tmp/milvus-utils main.go

mod-graph:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go mod graph

go:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go $(CMD)

run:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go run main.go $(CMD)

envs:
	podman run $(PODMAN_FLAGS) $(CONTAINER_IMAGE) go run main.go envs

clean:
	podman volume rm go-mod-cache go-build-cache 2>/dev/null || true
	@echo "Cleared cached volumes"
