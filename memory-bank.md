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

Full restore flow documented here. Extended downtime (10+ min) is acceptable for disaster recovery.

### Approach: Delete-and-Recreate

Instead of incrementally patching the Milvus CR, the restore flow **deletes all Milvus-related resources** in the namespace (CR, etcd Helm release, PVCs, HPAs, ScaledObjects) and **recreates the Milvus CR from scratch** with etcd scaled to 1 with startFromSnapshot and all Milvus workers at 0 replicas. This is cleaner and avoids operator race conditions during partial teardown.

**Key principle:** The namespace itself is preserved (keeps RBAC, secrets, service accounts). Only Milvus workload resources are deleted.

### CR Sanitization Rules (when recreating)

When the saved CR is prepared for re-creation, the following modifications are applied:

| Category                                                                                | Action                                             |
| --------------------------------------------------------------------------------------- | -------------------------------------------------- |
| `metadata.resourceVersion`, `uid`, `creationTimestamp`, `generation`                    | **Strip** (server-managed)                         |
| `metadata.finalizers`                                                                   | **Strip** (operator adds them)                     |
| `metadata.labels` (all)                                                                 | **Strip** (Flux re-adds on reconcile)              |
| `metadata.annotations` matching `milvus.io/*`                                           | **Strip** (operator regenerates)                   |
| `spec.dependencies.etcd.endpoints`                                                      | **Remove** (let operator derive from replicaCount) |
| `spec.dependencies.etcd.inCluster.values.startFromSnapshot`                             | **Set** with new PVC name + enabled: true          |
| `spec.dependencies.etcd.inCluster.values.replicaCount`                                  | **Set to 1**                                       |
| `spec.components.{proxy,mixCoord,dataNode,queryNode,streamingNode,standalone}.replicas` | **Set to 0**                                       |
| `status`                                                                                | **Strip entirely** (server-managed)                |

### Restore Flow (Revised)

1. **Resolve snapshot**: Latest complete, `--snapshot-id` flag, snapshot_id from config, or SNAPSHOT_ID env
2. **Confirm snapshot**: User confirms snapshot ID [Gate 1]. Overridden by `--force`.
3. **Suspend Flux**: Patch the configured flux kustomization with `.spec.suspend: true`
4. **Read live Milvus CR**: Save the full unstructured spec in memory (needed for recreation).
5. **Confirm destructive actions**: Confirm Milvus CR name and namespace [Gate 2]. Overridden by `--force`.
6. **Delete all Milvus resources**:
   - Delete HPAs in namespace
   - Delete KEDA ScaledObjects in namespace
   - Delete the Milvus CR (operator cascade-deletes Milvus deployments/services; etcd retained due to `deletionPolicy: Retain`)
   - Uninstall etcd Helm release (`{operatorName}-etcd`) — required because `deletionPolicy: Retain` keeps etcd alive after CR deletion
   - Delete etcd PVCs/PVs by label selector — ensures empty data dir for startFromSnapshot
7. **Wait for all pods to terminate**: Poll until no pods with `app.kubernetes.io/instance={operatorName}` exist.
8. **Delete S3 data**: Delete all objects under `{root_bucket}/{root_path}/`.
9. **Copy S3 data from snapshot**: Server-side copy from `{backup_bucket}/{backup_s3_path}/{snapshot_id}/` → `{root_bucket}/{root_path}/`.
10. **Seed etcd snapshot**: Create temp PVC + download Job to fetch etcd snapshot from S3.
11. **Recreate Milvus CR**: Apply the sanitized CR (see CR Sanitization Rules above).
12. **Wait for etcd-0 to be ready**: Watch StatefulSet until ready replicas is equal to 1.
13. **Revert startFromSnapshot**: Patch Milvus CR to null out `startFromSnapshot` (set to `{}`). Leave `replicaCount` as-is — Flux will set it on reconcile.
14. **Resume Flux**: Flux reconciles CR back to Git state, scales up workers, sets etcd replicaCount, recreates HPAs/ScaledObjects.
15. **Cleanup**: Delete temp PVC + Job.

### Restore Config

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

- **Delete-and-recreate** (not patch): Avoids operator race conditions; gives clean slate for etcd restore
- **Read CR before deletion**: Preserves the full production spec so we can recreate accurately
- **CR sanitization**: Strip server-managed metadata, labels, annotations, status, etcd endpoints; set replicas/startFromSnapshot
- **All Milvus workers at 0**: Only etcd starts initially; Flux handles worker scale-up after resume
- **CLI nulls startFromSnapshot**: After etcd is healthy, CLI patches startFronSnapshot to `{}` so Flux sees clean state
- **Flux handles replicaCount**: CLI does NOT revert replicaCount — Flux reconciles it to Git-defined value
- **etcd Helm uninstall required**: `deletionPolicy: Retain` means CR deletion alone won't clean up etcd
- **etcd endpoints removed**: Let operator derive endpoints from replicaCount (avoids stale pod references)
- **Bitnami `startFromSnapshot`**: Official chart mechanism for etcd restore
- **Single-replica bootstrap**: Restore etcd as 1 replica (EBS RWO compatible), then Flux scales up
- **EBS RWO confirmed safe**: Sequential access (Job→etcd-0) works since only one pod mounts at a time
- **Milvus Operator**: `zilliztech/milvus-operator`, API: `milvus.io/v1beta1`
- **Flux handles worker scale-up**: After resume, Flux reconciles CR to Git state (full replicas, HPAs, ScaledObjects)
- **2 confirmation gates**: Snapshot selection + pre-destructive. Overridden by `--force`.
- **Interactive + Non-Interactive**: `make run CMD="snapshot restore"` vs `make run CMD="snapshot restore --snapshot-id ... --force"`

### Etcd `startFromSnapshot` Mechanism

Note that this is an internal helm chart feature that ships with the Milvus operator.

1. Mounts snapshot PVC in init container
2. Copies snapshot file to known path
3. Main entrypoint detects snapshot + empty data dir → runs `etcdctl snapshot restore`
4. Handles member naming/cluster config automatically
5. After etcd-0 is healthy, CLI nulls startFromSnapshot by setting it to `{}`, then Flux scales to full replicas

### Temp Resources

| Resource                           | Purpose                                     | Cleanup               |
| ---------------------------------- | ------------------------------------------- | --------------------- |
| PVC `milvus-restore-snapshot-<id>` | Holds etcd snapshot for `startFromSnapshot` | Deleted after restore |
| Job `milvus-restore-download-<id>` | Downloads snapshot from S3 to PVC           | Deleted after restore |

### Flux Strategy

- Pause and resume the Kustomization specified in config (`restore.flux_kustomization_name`)
- HPAs/ScaledObjects are deleted as part of full resource cleanup — Flux recreates on resume
- CLI nulls `startFromSnapshot` by setting it to `{}` before Flux resume so Flux sees clean etcd config
- After resume, Flux reconciles CR back to Git-defined state (workers, scalers, replicaCount, etc.)

### Kubernetes Deployment

- **Snapshot create:** Runs as CronJob in EKS
- **Snapshot restore:** Manual Job or interactive CLI invocation
- **IRSA** for S3 access (AWS credentials from service account annotation)
- In-cluster K8s client (uses pod service account)
- RBAC needed: Milvus CR (get/delete/create/patch), etcd Helm release (uninstall), etcd PVCs (delete/list), HPAs (list/delete), KEDA ScaledObjects (list/delete), Flux Kustomization (patch), Jobs (create/delete), PVCs (create/delete)

### Implementation Changes Required

**`internal/k8s/client.go`:**

- Add `DeleteMilvusCR(ctx, name, namespace)` — deletes the Milvus CR
- Add `CreateMilvusCR(ctx, namespace, obj)` — creates a Milvus CR from unstructured object
- Remove `ScaleDownMilvus()` (no longer needed — we delete the entire CR)
- Keep: `DeleteHPAs`, `DeleteScaledObjects`, `DeleteEtcdResources`, `CreateTempPVC`, `CreateDownloadJob`, `WaitForJobComplete`, `WaitForPodsTerminated`, `WaitForStatefulSetReady`, `DeleteJob`, `DeletePVC`, `SuspendFlux`, `ResumeFlux`, `GetMilvusCR`, `PatchMilvusCR`

**`cmd/restore.go`:**

- Rewrite `runRestore()` to follow revised flow
- Add helper `buildRestoreCR(originalCR, pvcName)` — sanitizes CR + sets 0 replicas + startFromSnapshot
- Add helper to null out startFromSnapshot (simple JSON merge patch)
- Remove old `buildStartFromSnapshotPatch()` (replaced by `buildRestoreCR`)

---

## Progress

**Current Status:** Phase 8 complete (delete-and-recreate approach implemented). Phase 9 next.

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
| 9     | Testing & CI                       | ⬜ Next |

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

### Phase 8 Tasks (Restore Orchestration) — COMPLETE

- [x] Add `RestoreConfig` to Config struct with mapstructure tags
- [x] Resolve snapshot: latest complete, --snapshot-id flag, config, env
- [x] Interactive confirmation gates (skipped with --force)
- [x] Rewrite restore flow: delete-and-recreate approach
- [x] Add `DeleteMilvusCR()` to k8s client
- [x] Add `CreateMilvusCR()` to k8s client
- [x] Remove `ScaleDownMilvus()` from k8s client
- [x] Add `buildRestoreCR()` helper (modifies saved CR: 0 replicas + startFromSnapshot)
- [x] Add `buildRevertStartFromSnapshotPatch()` helper (nulls startFromSnapshot for Flux)
- [x] Rewrite `runRestore()` with revised flow (read CR → delete all → wait pods → S3 → seed etcd → create CR → wait etcd → revert patch → resume Flux → cleanup)

### What's Next

Phase 9: Testing & CI.

### Future Plans

- Use S3 batch operations to make the copies faster once the whole implementation is done.
- After a successful backup, place a file named as a tamestamp in the s3 snapshot directory which tells if the s3 snapshot was complete or not.
