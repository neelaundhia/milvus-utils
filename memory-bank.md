# Memory Bank — milvus-utils

## Project Goal

Build a production-ready CLI tool that creates and restores **raw S3 + etcd snapshots** for Milvus 2.5.x+ running on EKS. The CLI tool should be accessible locally in an interactive fashion with configuration/confirmation gates as well as in-cluster as a CronJob with flags that can override interactive configuration/confirmation gates.

**Problem:** Milvus's built-in backup tool triggers index rebuilding on restore, which is slow and resource-intensive. Raw snapshots bypass this by capturing etcd metadata and S3 segment data as-is, preserving pre-built indexes.

**Constraints:**

- Runs inside EKS pod; credential chain is IRSA (no explicit creds)
- All endpoints derived from `milvus.operator_name` (or `localhost` when `milvus.local: true`)
- No K8s scaling during create — only write quiescing via Milvus API
- CLI built with Cobra + Viper; config from YAML + env vars

---

## Language & Tooling

- **Go 1.25** (CI enforced)
- **Dev container:** `mcr.microsoft.com/devcontainers/go:2-1.25-trixie`

## Dependencies

| Package                                                   | Purpose                          |
| --------------------------------------------------------- | -------------------------------- |
| `github.com/spf13/cobra`                                  | CLI framework                    |
| `github.com/spf13/viper`                                  | Config loading (YAML + env vars) |
| `github.com/sirupsen/logrus`                              | Structured logging               |
| `github.com/milvus-io/milvus/client/v2 v2.6.3`            | Milvus gRPC SDK                  |
| `go.etcd.io/etcd/client/v3 v3.5.5`                        | Etcd Maintenance API             |
| `github.com/aws/aws-sdk-go-v2`                            | AWS SDK core                     |
| `github.com/aws/aws-sdk-go-v2/config`                     | AWS default credential chain     |
| `github.com/aws/aws-sdk-go-v2/service/s3`                 | S3 API client                    |
| `github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager` | Multipart upload/download        |
| `golang.org/x/sync`                                       | errgroup for parallel S3 ops     |
| `k8s.io/client-go`                                        | Kubernetes API client            |
| `k8s.io/apimachinery`                                     | Unstructured objects, API types  |
| `sigs.k8s.io/controller-runtime/pkg/client`               | Dynamic/unstructured K8s client  |

---

## Configuration

Config is loaded in layers (later overrides earlier):

1. `config.yaml` (from `.` or `/config`)
2. `secrets.yaml` (merged if present)
3. `--config <file>` flag (merged if provided)
4. Environment variables (auto-mapped, `.` → `_`)

See `README.md` for the full config YAML reference and derived endpoints.

### Derived Endpoint Helpers

`MilvusConfig` exposes helper methods — always use these, never build addresses inline:

| Method            | local: false                   | local: true        |
| ----------------- | ------------------------------ | ------------------ |
| `GRPCAddr()`      | `{operator_name}-milvus:19530` | `localhost:19530`  |
| `EtcdEndpoints()` | `[{operator_name}-etcd:2379]`  | `[localhost:2379]` |

Mgmt HTTP is derived in `NewClient` from gRPC host + port `9091`. Milvus CR: `milvus.io/v1beta1 / Kind: Milvus / name: {operator_name}`.

---

## CLI Structure (Cobra)

```
milvus-utils
├── snapshot
│   ├── create    — quiesce + etcd snapshot + S3 copy
│   ├── restore   — etcd restore + S3 restore via K8s
│   └── list      — list available snapshots
└── envs          — print resolved config env var keys
```

Each subcommand lives in its own file under `cmd/`. The above is not an exhaustive list. It just shows the one we are largely interested in.

## Config Pattern (Viper)

- `Config` struct in `cmd/root.go` with `mapstructure` tags
- `setDefaults()` walks struct and registers `default` struct tags with viper
- `bindEnvs()` walks struct and binds env vars (e.g. `MILVUS_OPERATOR_NAME`)
- `loadConfig()` unmarshals viper state into `Config` struct

## Internal Package Pattern

Each `internal/` package follows:

```go
type Client struct { ... }
func NewClient(ctx context.Context, ...) (*Client, error) { ... }
func (c *Client) Close() { ... }
```

Methods split across files by concern (e.g. `client.go` + `management.go`, `client.go` + `parallel.go`).

## Error Handling

- `RunE` functions return errors (Cobra prints them)
- Deferred calls for cleanup (e.g., restoring `deny.writing = false`)
- Logrus for structured logging at each step

---

## S3 Patterns

### Naming Conventions

```
Etcd snapshot:   s3://{backup_bucket}/{backup_etcd_path}/{snapshot_id}.db
S3 snapshot:     s3://{backup_bucket}/{backup_s3_path}/{snapshot_id}/
Production data: s3://{root_bucket}/{root_path}/
```

Snapshot IDs are timestamp strings: `2006-01-02T15-04-05Z` (Go time format).

### Parallelization

S3 operations use `errgroup` with configurable concurrency (default 64 workers):

- **CopyPrefix:** Lists source objects, parallel `CopyObject` (server-side). Logs every 1000 objects.
- **DeletePrefix:** Lists objects, `DeleteObjects` in batches of 1000 (S3 API limit), batches in parallel.
- **Upload:** `transfermanager.UploadObject` for automatic multipart upload.
- **Download:** Streams via `GetObject` + `io.Copy`.

`ParseBucketURI()` strips the `s3://` prefix from config bucket URIs.

---

## Snapshot Create — Engineering Details

See `README.md` for the user-facing flow steps. Below are the ordering rationale and GC mechanism details.

### Ordering Rationale

Deny writing must come _before_ flush to prevent new writes sneaking in between flush and snapshot. GC is paused before flush to eliminate edge cases where in-flight compaction marks segments as dropped during flush.

### GC Pause/Resume

Managed via **management HTTP API** (port 9091), not gRPC:

| Operation | Endpoint                                                               | Returns                       |
| --------- | ---------------------------------------------------------------------- | ----------------------------- |
| Pause GC  | `GET /management/datacoord/garbage_collection/pause?pause_seconds=<N>` | `{"msg":"OK","ticket":"..."}` |
| Resume GC | `GET /management/datacoord/garbage_collection/resume?ticket=<ticket>`  | `{"msg":"OK"}`                |

**Ticket mechanism:** PauseGC returns an opaque ticket (base64-encoded JSON containing UUID token + collection_id). ResumeGC **must** receive this ticket — without it, server returns HTTP 500.

Notes:

- Pause requires a TTL (`pause_seconds`); server re-enables GC on expiry
- For long snapshots, renew before expiry (every `pause_seconds/2`)
- Pause failure is non-fatal (GC runs infrequently; short snapshots are safe without it)
- Compaction does **not** need separate pausing — deny-writing prevents new compactions, and compaction only creates files (GC deletes old ones)

---

## Snapshot Restore — Implementation Details

See `README.md` for the full restore flow steps. Extended downtime (10+ min) is acceptable for disaster recovery. Below are the implementation-specific design details.

### Key Design Decisions

- **Bitnami `startFromSnapshot`**: Official chart mechanism for etcd restore
- **Single-replica bootstrap**: Restore etcd as 1 replica (EBS RWO compatible), then scale up
- **Live CR capture**: Tool reads actual CR before deleting to preserve full user spec
- **Flux handles cleanup**: Temp `startFromSnapshot` config removed on reconcile; HPAs/ScaledObjects recreated
- **4 confirmation gates** at destructive steps (snapshot selection, CR delete, S3 delete, S3 restore)
- **Interactive as well as Non-Interactive**: The tool should be ablet to run interactively with prompts as well as non-interactively with flags for configuration/confirmation. For example, `make run CMD="snapshot restore` (Interactive prompts for configuration and confirmation gates) `make run CMD="snapshot restore --snapshot-id 2025-04-29T10-00-00Z --force` \*(Non-Interactive with flags for providing needed values)

### Etcd `startFromSnapshot` Mechanism

1. Mounts snapshot PVC in init container
2. Copies snapshot file to known path
3. Main entrypoint detects snapshot + empty data dir → runs `etcdctl snapshot restore`
4. Handles member naming/cluster config automatically
5. After etcd-0 is healthy, scale to full replicas (new members join via `etcdctl member add`)

### Temp Resources

| Resource                           | Purpose                                     | Cleanup               |
| ---------------------------------- | ------------------------------------------- | --------------------- |
| PVC `milvus-restore-snapshot-<id>` | Holds etcd snapshot for `startFromSnapshot` | Deleted after restore |
| Job `milvus-restore-download-<id>` | Downloads snapshot from S3 to PVC           | Deleted after restore |

### Flux Isolation Strategy

- Suspend only the specific Kustomization managing Milvus (not entire infrastructure)
- HPAs/ScaledObjects are deleted (not annotated) — Flux recreates on resume
- After resume, Flux reconciles CR back to Git-defined state

### Kubernetes Deployment

- **Snapshot create:** Runs as CronJob in EKS
- **Snapshot restore:** Manual Job or interactive CLI invocation
- **IRSA** for S3 access (AWS credentials from service account annotation)
- In-cluster K8s client (uses pod service account)
- RBAC needed: Milvus CR (get/delete/create/patch), etcd PVCs (delete/list), HPAs (list/delete), KEDA ScaledObjects (list/delete), Flux Kustomization (patch), Jobs (create/delete), PVCs (create/delete)

---

## Progress

**Current Status:** Phase 6 complete. Snapshot list command implemented and verified.

### Phase Checklist

| Phase | Description                        | Status  |
| ----- | ---------------------------------- | ------- |
| 0     | Project foundation & documentation | ✅ Done |
| 1     | Config & types refactor            | ✅ Done |
| 2     | Milvus client                      | ✅ Done |
| 3     | Etcd snapshot client               | ✅ Done |
| 4     | S3 operations                      | ✅ Done |
| 5     | Snapshot create orchestration      | ✅ Done |
| 6     | Snapshot list command              | ✅ Done |
| 7     | K8s client for restore             | ⬜ Next |
| 8     | Snapshot restore orchestration     | ⬜      |
| 9     | Kubernetes deployment manifests    | ⬜      |
| 10    | Testing & CI                       | ⬜      |

### Phase 7 Tasks (K8s Client)

- [ ] Create `internal/k8s/client.go`
- [ ] Flux suspend/resume: patch Kustomization `.spec.suspend`
- [ ] Read live Milvus CR (unstructured client, preserves full spec)
- [ ] Delete Milvus CR + wait for all pods to terminate
- [ ] Delete etcd PVCs by label selector (`app.kubernetes.io/instance={operator_name}`)
- [ ] Delete HPAs and KEDA ScaledObjects in namespace
- [ ] Create temporary PVC + manage Job to download snapshot to PVC
- [ ] Apply modified Milvus CR (with `startFromSnapshot` + `replicaCount: 1`)
- [ ] Patch Milvus CR (remove `startFromSnapshot`, restore original `replicaCount`)
- [ ] Wait helpers: pods terminated, etcd ready, Milvus healthy
- [ ] Cleanup: delete temp PVC + Job

### What's Next

Begin Phase 7: K8s client implementation for restore operations.
