# AGENTS.md — milvus-utils

## Project Overview

`milvus-utils` is a CLI tool that creates and restores raw S3 + etcd snapshots for Milvus 2.5.x+. It is designed to run as a Kubernetes CronJob in EKS.

- **Snapshot create:** Uses the Milvus SDK API (Flush + `database.force.deny.writing`) to quiesce writes, snapshots etcd via the Maintenance API, and copies S3 data server-side — no K8s scaling required.
- **Snapshot restore:** Patches the Milvus operator CR, deletes etcd PVCs, restores etcd snapshot and S3 data, then restarts via Flux reconcile.
- **Raw snapshots preserve pre-built indexes**, solving the reindex-on-restore problem.

## Repository Layout

```
cmd/            — Cobra CLI commands (root, snapshot, create, restore, envs)
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
  k8s/          — Kubernetes client (CR patching, STS scaling, PVC deletion)
pkg/            — Shared utilities
deploy/         — Kubernetes manifests (CronJob, RBAC, ConfigMap)
```

## Key Conventions

- **Module:** `github.com/neelaundhia/milvus-utils`
- **Go version:** 1.25 (CI) / dev container `mcr.microsoft.com/devcontainers/go:2-1.25-trixie`
- Go commands run natively inside the dev container — Makefile targets call `go` directly.
- **Config** is loaded from `config.yaml` + optional `secrets.yaml` + `--config` flag + env vars (viper, cobra).
- **Logging:** `github.com/sirupsen/logrus` — JSON in production, text for local dev.
- Use `make build`, `make tidy`, `make test`, etc. as the canonical entry points.

## Derived Endpoints (from `milvus.operator_name`)

| Resource         | Address                               |
| ---------------- | ------------------------------------- |
| Milvus gRPC      | `{operator_name}-milvus:{port}`       |
| Milvus Mgmt HTTP | `http://{operator_name}-milvus:9091`  |
| Etcd             | `{operator_name}-etcd:2379`           |
| Milvus CR (K8s)  | Kind `Milvus`, name `{operator_name}` |

The management HTTP API is derived automatically inside `internal/milvus.NewClient` by extracting the host from the gRPC address and hard-coding port `9091`. No config field is required.

## Development Workflow

```bash
make build        # compile binary
make test         # run unit tests
make tidy         # go mod tidy
make envs         # print resolved config keys
make run CMD="snapshot create"   # run a command
make clean        # remove build artifacts
```

## Implementation Phases (see memory-bank/progress.md)

| Phase | Description                        | Status      |
| ----- | ---------------------------------- | ----------- |
| 0     | Project foundation & documentation | Complete    |
| 1     | Config & types refactor            | Complete    |
| 2     | Milvus client                      | Complete    |
| 3     | Etcd snapshot client               | Complete    |
| 4     | S3 operations                      | Complete    |
| 5     | Snapshot create orchestration      | Complete    |
| 6     | Snapshot list command              | Complete    |
| 7     | K8s client for restore             | Not started |
| 8     | Snapshot restore orchestration     | Not started |
| 9     | Kubernetes deployment manifests    | Not started |
| 10    | Testing & CI                       | Not started |

## Memory Bank

All project documentation lives in `memory-bank/`. **Always read `memory-bank/progress.md` at the start of every session.**

### Current file tree

```
memory-bank/
  progress.md       — phase checklist, current status, what's next
  projectbrief.md   — goal, problem statement, core capabilities
  techContext.md    — dependencies, config structure, derived endpoints, build commands
  systemPatterns.md — CLI structure, error handling, S3 naming, orchestration flows, open questions
```

Update this tree whenever a file is added to or removed from `memory-bank/`.

## Agent Guidelines

- Read `memory-bank/progress.md` at the start of every session to understand current state.
- **After completing any task:**
  1. Update `memory-bank/progress.md` — mark items done, set "What's Next".
  2. Go through each and every file in `memory-bank/` and update any other `memory-bank/` file whose content has changed with the work you did. These changes can be updated dependencies, architecture changes, file structure changes, etc. It is very very important to keep the memory bank updated.
  3. Update the memory bank file tree in this file (`AGENTS.md`) if files were added or removed.
  4. Update `README.md` if user-facing behaviour, config structure, or usage changed.
- Follow existing code style — no extra abstractions, no speculative features.
- All external dependency additions go through `make tidy`.
- Secrets (`config.yaml`, `secrets.yaml`) are gitignored — never commit them.

## Snapshot Create Flow Order

The correct quiesce order for consistency:

1. **Deny writing** — blocks new inserts/deletes/upserts (clean cutoff)
2. **Pause GC** — prevents S3 object deletion during snapshot
3. **Flush** — persists all in-memory segments to S3
4. **Snapshot etcd + copy S3**
5. **Resume GC** (with ticket from step 2)
6. **Allow writing** (deferred, always runs)

Deny writing must come _before_ flush to prevent new writes sneaking in between flush and snapshot. GC is paused before flush to eliminate any edge case where an in-flight compaction marks segments as dropped during flush.

## GC Ticket Mechanism

PauseGC returns an opaque **ticket** (base64-encoded token + collection_id). This ticket **must** be passed to ResumeGC. Without it, the server returns HTTP 500 (`strconv.ParseInt: parsing "": invalid syntax`).
