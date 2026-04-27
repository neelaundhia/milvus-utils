# Progress — milvus-utils

## Current Status

**Phase 5 complete.** Snapshot create orchestration implemented and **integration-tested** against live Milvus + etcd + S3. All 6 steps verified: list DBs, deny writing, pause GC, flush, etcd snapshot, S3 copy (2389 objects in ~5s). AWS config (region/endpoint) added. Build verified clean.

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

### Phase 4: S3 Operations ✅

- [x] Create `internal/s3/client.go` — Client struct, NewClient, ParseBucketURI, ListObjects, ListCommonPrefixes, Upload (transfermanager), Download
- [x] Create `internal/s3/parallel.go` — CopyPrefix (parallel server-side copy, errgroup worker pool), DeletePrefix (parallel batch delete, 1000 keys/batch)
- [x] Add `github.com/aws/aws-sdk-go-v2` + `config` + `service/s3` + `feature/s3/transfermanager` dependencies
- [x] Add `golang.org/x/sync` dependency (errgroup for parallel operations)
- [x] `make tidy` + `make build` verified clean

### Phase 5: Snapshot Create Orchestration ✅

- [x] Implement `cmd/create.go` `runCreate` function with full orchestration
- [x] Generate snapshot ID from UTC timestamp (`2006-01-02T15-04-05Z` format)
- [x] Initialise all three clients (Milvus gRPC, etcd, S3) from config
- [x] Step 1: Deny writing on all databases (with deferred allow-writing on all DBs)
- [x] Step 2: Pause GC with 3600s TTL (non-fatal on failure; deferred ResumeGC with ticket)
- [x] Step 3: Flush all databases (persists in-memory segments to S3)
- [x] Step 4a: Snapshot etcd to in-memory buffer via Maintenance API
- [x] Step 4b: Upload etcd snapshot to `s3://{backup_bucket}/{backup_etcd_path}/{snapshot_id}.snapshot`
- [x] Step 4c: Parallel server-side copy of S3 data from `{root_bucket}/{root_path}/` to `{backup_bucket}/{backup_s3_path}/{snapshot_id}/`
- [x] Cleanup: GC resumed + writes re-enabled by defers (runs even on error)
- [x] Structured logging at each step with logrus
- [x] Added `AWSConfig` struct (region, endpoint) to `cmd/root.go`
- [x] Added `WithRegion` / `WithEndpoint` options to S3 `NewClient`
- [x] Added `ListCollections` method in `internal/milvus/collections.go`
- [x] Added `InitConfig()` exported function for standalone scripts
- [x] Integration tested all 6 steps against live services
- [x] `make build` verified clean

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

**Phase 6** — Create `internal/k8s/client.go`. Implement Kubernetes client for restore operations: CR patching, etcd STS scaling, PVC deletion, Flux annotation toggling.
