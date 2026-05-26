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

Full restore flow is documented here (moved from README until implemented). Extended downtime (10+ min) is acceptable for disaster recovery. Below are the implementation-specific design details.

### Restore Flow

1. **Resolve snapshot**: Latest complete, `--snapshot-id` flag, snapshot_id from config, or SNAPSHOT_ID env
2. **Confirm snapshot**: User confirms snapshot ID [Gate 1]. Overridden by --force flag when run non-interactively.
3. **Suspend Flux**: Patch the configured flux kustomization (`restore.`) with `.spec.suspend: true`
4. **Confirm Milvus instance before performing destructive actions**: Confirm Milvus CR name and namespace [Gate 2]. Step 5-8 are destructive, prompt the user and get explicit confirmation. Overridden by --force flag when run non-interactively.
5. **Delete scalers**: Delete all HPAs + KEDA ScaledObjects in the configured namespace (`milvus.namespace`)
6. **Scale down Milvus workers to 0**: Patch the Milvus CR to set all component replicas to 0 (proxy, mixCoord, rootCoord, dataCoord, queryCoord, indexCoord, dataNode, queryNode, indexNode, streamingNode, standalone).
7. **Uninstall etcd Helm release + delete PVCs/PVs**: Use Helm SDK to uninstall `{operatorName}-etcd`, then delete PVCs/PVs by label `app.kubernetes.io/instance={operatorName}-etcd,app.kubernetes.io/name=etcd`.
8. **Delete S3 files**: Delete all the Milvus S3 files.
9. **Wait**: Wait till there are no pods in the namespace.
10. **Copy S3**: Make a server side copy of S3 files from the selected snapshot to Milvus S3 files.
11. **Seed etcd snapshot to PVCS**: Temp PVC + Job downloads snapshot from S3
12. **Recreate Milvus CR**: Recreate Milvus CR and set etcd `replicaCount: 1` + Bitnami `startFromSnapshot`
13. **Wait etcd-0**: Single replica restores from snapshot
14. **Resume Flux**: Reconciles CR to Git state, recreates scalers.
15. **Wait healthy**: All etcd members + Milvus workers
16. **Cleanup**: Delete temp PVC + Job

### Restore Config (to be added to Config struct in Phase 7)

```yaml
restore:
  snapshot_id: "" # override snapshot to restore (default: latest complete)
  storage_class: "" # storage class for temp PVC
  job_service_account: "" # SA with IRSA for S3 read access to backup bucket
  job_image: "amazon/aws-cli" # image for snapshot download Job
  flux_kustomization_name: "" # Flux Kustomization to suspend
  flux_kustomization_namespace: "" # namespace of Flux Kustomization
```

### Key Design Decisions

- **Bitnami `startFromSnapshot`**: Official chart mechanism for etcd restore
- **Single-replica bootstrap**: Restore etcd as 1 replica (EBS RWO compatible), then scale up
- **EBS RWO confirmed safe**: EBS does NOT support RWX; sequential access (Job→etcd-0) works with RWO since only one pod mounts at a time
- **Milvus Operator**: Now at `zilliztech/milvus-operator` (original `milvus-io/milvus-operator` archived Nov 2023). API: `milvus.io/v1beta1`
- **CR patch (not delete/recreate)**: Scale down and startFromSnapshot are applied as JSON merge patches to the existing Milvus CR. Flux resume reconciles back to Git state.
- **etcd deployed as Helm release by operator**: Operator deploys etcd via `helm install {operatorName}-etcd`. Uninstalled via Helm SDK (`action.Uninstall`).
- **Flux handles cleanup**: Temp `startFromSnapshot` config removed on reconcile; HPAs/ScaledObjects recreated
- **2 confirmation gates** at destructive steps (snapshot selection, pre-destructive confirmation). Overridden by `--force`.
- **Interactive as well as Non-Interactive**: The tool should be able to run interactively with prompts as well as non-interactively with flags for configuration/confirmation. For example, `make run CMD="snapshot restore"` (Interactive prompts for configuration and confirmation gates) `make run CMD="snapshot restore --snapshot-id 2025-04-29T10-00-00Z --force"` (Non-Interactive with flags for providing needed values)

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

**Current Status:** Phase 8 complete. Snapshot restore orchestration implemented.

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
| 7     | K8s client for restore             | ✅ Done |
| 8     | Snapshot restore orchestration     | ✅ Done |
| 9     | Kubernetes deployment manifests    | ⬜ Next |
| 10    | Testing & CI                       | ⬜      |

### Phase 7 Tasks (K8s Client)

- [x] Create `internal/k8s/client.go`
- [x] Flux suspend/resume: patch Kustomization `.spec.suspend`
- [x] Read live Milvus CR (unstructured client, preserves full spec)
- [x] PatchMilvusCR: JSON merge patch on the Milvus CR
- [x] Delete etcd: Helm SDK uninstall (`{operatorName}-etcd`) + PVCs/PVs by label selector
- [x] Delete HPAs and KEDA ScaledObjects in namespace
- [x] Create temporary PVC + manage Job to download snapshot to PVC
- [x] Scale down all Milvus CR components to 0 replicas (patches CR, not deployments)
- [x] Wait helpers: pods terminated, etcd ready, Milvus healthy
- [x] Cleanup: delete temp PVC + Job

### Phase 8 Tasks (Restore Orchestration)

- [x] Add `RestoreConfig` to Config struct with mapstructure tags
- [x] Wire restore command with full orchestration flow
- [x] Resolve snapshot: latest complete, --snapshot-id flag, config, env
- [x] Interactive confirmation gates (skipped with --force)
- [x] `buildStartFromSnapshotPatch()` returns JSON patch for etcd single-replica bootstrap
- [x] Full end-to-end flow: Flux suspend → scalers delete → scale down via CR → helm uninstall etcd → S3 delete → wait → S3 copy → seed etcd → patch CR with startFromSnapshot → wait etcd → resume Flux → cleanup

### What's Next

Begin Phase 9: Kubernetes deployment manifests (CronJob, RBAC, ConfigMap).

### Future Plans

- Use S3 batch operations to make the copies faster once the whole implementation is done.
- After a successful backup, place a file named as a tamestamp in the s3 snapshot directory which tells if the s3 snapshot was complete or not.
