# Progress — milvus-utils

## Current Status

**Phase 3 complete.** Etcd snapshot client implemented. `go.etcd.io/etcd/client/v3` promoted to direct dependency. Build verified clean.

---

## Phase Checklist

### Phase 0: Project Foundation & Documentation ✅

- [x] Created `AGENTS.md` at repo root with Memory Bank section, file tree, and post-task guidelines
- [x] Created `memory-bank/` with `projectbrief.md`, `techContext.md`, `systemPatterns.md`, `progress.md`
- [x] Updated `.gitignore` to fully track `memory-bank/` files
- [x] Recreated `go.mod` with Go 1.25 and only needed dependencies (cobra, viper, logrus)
- [x] `go mod tidy` run to pin versions and generate `go.sum`
- [x] Deleted redundant `plan.md` (content absorbed into memory-bank)
- [x] `make build` verified clean

### Phase 1: Configuration & Types Refactor ✅

- [x] Extended `Config` struct in `cmd/root.go` with `MilvusConfig` sub-struct
- [x] Added `k8s.io/apimachinery v0.32.3` and `k8s.io/client-go v0.32.3` to `go.mod`
- [x] `go mod tidy` run — `go.sum` updated
- [x] `make envs` shows all new Milvus config keys (`MILVUS_OPERATOR_NAME`, `MILVUS_NAMESPACE`, `MILVUS_ROOT_BUCKET`, etc.)
- [x] `make build` verified clean

### Phase 2: Milvus Client ✅

- [x] Create `internal/milvus/client.go` — gRPC SDK methods (Flush, SetDeny\*)
- [x] Create `internal/milvus/management.go` — HTTP management API methods (PauseGC, ResumeGC)
- [x] Add `github.com/milvus-io/milvus/client/v2 v2.6.3` dependency
- [x] Add `PauseGC` / `ResumeGC` via management HTTP API (`/management/datacoord/garbage_collection/pause|resume`)
  - Management URL derived from gRPC addr (same host, port 9091) — no new config field
  - Uses stdlib `net/http` — no new dependency
  - gRPC and HTTP code separated into distinct files
- [x] GC ticket mechanism: PauseGC returns opaque ticket, ResumeGC requires it
- [x] `make build` verified clean

### Phase 3: Etcd Snapshot Client ✅

- [x] Create `internal/etcd/client.go` — `NewClient`, `Close`, `Snapshot(ctx, io.Writer)`
- [x] Promote `go.etcd.io/etcd/client/v3 v3.5.5` to direct dependency
- [x] `make build` verified clean
- [x] Create `tests/etcd/main.go` — ad-hoc smoke test (port-forward to `localhost:2379`)
- [x] Create `tests/milvus/main.go` — ad-hoc smoke test covering all client methods (port-forward to `localhost:19530` + `9091`)

### Phase 4: S3 Operations ⬜

- [ ] Create `internal/s3/client.go`
- [ ] Add `github.com/aws/aws-sdk-go-v2` dependency

### Phase 5: Snapshot Create Orchestration ⬜

- [ ] Implement `cmd/create.go` RunE

### Phase 6: K8s Client for Restore ⬜

- [ ] Create `internal/k8s/client.go`

### Phase 7: Snapshot Restore Orchestration ⬜

- [ ] Implement `cmd/restore.go` RunE
- [ ] Resolve etcd snapshot seeding mechanism (see systemPatterns.md)

### Phase 8: Snapshot List Command ⬜

- [ ] Create `cmd/list.go`

### Phase 9: Kubernetes Deployment Manifests ⬜

- [ ] Create `deploy/` directory with CronJob, RBAC, ConfigMap, SA

### Phase 10: Testing & CI ⬜

- [ ] Unit tests for all `internal/` packages
- [ ] Update CI to Go 1.25
- [ ] `make test` passes

---

## What's Next

**Phase 4** — Create `internal/s3/client.go`. Add `github.com/aws/aws-sdk-go-v2` dependency. Implement server-side S3 copy, upload (for etcd snapshot), and download methods.
