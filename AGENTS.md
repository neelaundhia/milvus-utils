# AGENTS.md — milvus-utils

## Project Overview

`milvus-utils` is a CLI tool that creates and restores **raw S3 + etcd snapshots** for Milvus 2.5.x+ running on EKS (Kubernetes CronJob). Raw snapshots preserve pre-built indexes, solving the reindex-on-restore problem.

## Documentation Map

| File             | Purpose                                                         |
| ---------------- | --------------------------------------------------------------- |
| `README.md`      | User-facing docs: config reference, usage, command descriptions |
| `memory-bank.md` | Engineering spec: design rationale, internal patterns, progress |
| `AGENTS.md`      | AI agent instructions: conventions, workflow, guidelines        |

## Repository Layout

```
cmd/            — Cobra CLI commands (root, snapshot, create, restore, list, envs)
internal/
  milvus/       — Milvus gRPC client (SDK v2) and management HTTP client
    client.go      — struct, NewClient, gRPC methods (Flush, SetDeny*)
    management.go  — HTTP management API methods (PauseGC, ResumeGC)
    collections.go — ListCollections per database
  etcd/
    client.go      — NewClient, Close, Snapshot(ctx, io.Writer)
  s3/           — AWS S3 client (server-side copy, upload, download)
    client.go      — struct, NewClient, ParseBucketURI, List*, Upload, Download
    parallel.go    — CopyPrefix (parallel copy), DeletePrefix (parallel batch delete)
  k8s/          — Kubernetes client (Flux suspend, CR lifecycle, PVC/HPA/SO deletion, Job management)
pkg/            — Shared utilities
deploy/         — Kubernetes manifests (CronJob, RBAC, ConfigMap)
```

## Key Conventions

- **Module:** `github.com/neelaundhia/milvus-utils`
- **Go version:** 1.25 (CI) / dev container `mcr.microsoft.com/devcontainers/go:2-1.25-trixie`
- Go commands run natively inside the dev container — Makefile targets call `go` directly.
- **Config:** `config.yaml` + optional `secrets.yaml` + `--config` flag + env vars (viper, cobra). See README.md for full config reference.
- **Logging:** `github.com/sirupsen/logrus` — JSON in production, text for local dev.

## Development Workflow

```bash
make build        # compile binary
make test         # run unit tests
make tidy         # go mod tidy
make envs         # print resolved config keys
make run CMD="snapshot create"   # run a command
make clean        # remove build artifacts
```

## Implementation Phases

| Phase | Description                        | Status      |
| ----- | ---------------------------------- | ----------- |
| 0     | Project foundation & documentation | Complete    |
| 1     | Config & types refactor            | Complete    |
| 2     | Milvus client                      | Complete    |
| 3     | Etcd snapshot client               | Complete    |
| 4     | S3 operations                      | Complete    |
| 5     | Snapshot create orchestration      | Complete    |
| 6     | Snapshot list command              | Complete    |
| 7     | K8s client for restore             | Complete    |
| 8     | Snapshot restore orchestration     | Complete    |
| 9     | Kubernetes deployment manifests    | Not started |
| 10    | Testing & CI                       | Not started |

## Agent Guidelines

- Read `memory-bank.md` at the start of every session to understand current state and design details.
- Refer to `README.md` for config structure, derived endpoints, and command usage.
- **After completing any task:**
  1. Update `memory-bank.md` — mark items done, set "What's Next", update any section whose content changed.
  2. Update `README.md` if user-facing behaviour, config structure, or usage changed.
- Follow existing code style — no extra abstractions, no speculative features.
- All external dependency additions go through `make tidy`.
- Secrets (`config.yaml`, `secrets.yaml`) are gitignored — never commit them.
