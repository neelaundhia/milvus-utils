# milvus-utils

CLI tool for managing Milvus vector-database instances in Kubernetes. Currently, it supports `snapshot create`, `snapshot list` and `snapshot restore` for speedy Milvus disaster recovery. The tool can be run both interactively as well as non-interactively. Contrary to the `milvus-backup` tool that basically inserts raw data and then creates indices, `milvus-utils` takes `raw snapshots of etcd and s3 state` and then restores them instead. Doing it this way makes DR feasible/efficient for large Milvus databases.

## Project Layout

Follows [golang-standards/project-layout](https://github.com/golang-standards/project-layout).

```
.
├── main.go              # delegates to cmd.Execute()
├── cmd/
│   ├── root.go          # Config struct, viper setup, logrus logger, reflection helpers
│   ├── envs.go          # `envs` subcommand: introspects Config via reflection
│   ├── snapshot.go      # `snapshot` parent command
│   ├── create.go        # `snapshot create` — quiesce + etcd snapshot + S3 copy
│   ├── list.go          # `snapshot list` — list and verify snapshots in S3
│   └── restore.go       # `snapshot restore`
├── internal/
│   ├── milvus/
│   │   ├── client.go      # Milvus gRPC SDK client (Flush, SetDenyWriting, etc.)
│   │   ├── management.go  # Milvus management HTTP client (PauseGC, ResumeGC)
│   │   └── collections.go # ListCollections per database
│   ├── etcd/
│   │   └── client.go     # Etcd Maintenance API client (Snapshot)
│   └── s3/
│       ├── client.go     # AWS S3 client (List, Upload, Download)
│       └── parallel.go   # Parallel server-side copy & batch delete
```

## Build and Test

Go commands run natively (dev container or local). Use the Makefile targets:

```shell
make build          # go build -o /tmp/milvus-utils main.go
make test           # go test ./...
make tidy           # go mod tidy
make run CMD="..."  # go run main.go <args>
make envs           # inspect available config environment variables
make clean          # remove build artifacts
```

## Configuration

Create `config.yaml` using the example below and fill in your values. Secrets (credentials) go in `secrets.yaml` (gitignored). Config is also configurable via environment variables (e.g. `MILVUS_OPERATOR_NAME`).

```yaml
log:
  level: info # debug|info|warn|error
  format: json # json|text

aws:
  region: "eu-west-1" # AWS region for S3 buckets
  endpoint: "" # optional: override S3 endpoint (e.g. LocalStack)

milvus:
  local: false # if true, use localhost for all endpoints (ignores operator_name/namespace)
  operator_name: "milvus" # derives all endpoints (gRPC, etcd, K8s CR)
  namespace: "milvus" # Kubernetes namespace for Milvus resources
  root_bucket: "s3://milvus" # production Milvus data bucket
  root_path: "files" # S3 prefix within root_bucket
  backup_bucket: "s3://milvus-backup"
  backup_etcd_path: "etcd-snapshots"
  backup_s3_path: "s3-snapshots"

restore:
  snapshot_id: "" # optional: override snapshot to restore (default: latest complete)
  storage_class: "" # storage class for temp PVC
  job_service_account: "" # SA with IRSA (or equivalent) for S3 read access to backup bucket
  job_image: "amazon/aws-cli" # image for snapshot download Job
  flux_kustomization_name: "" # Flux Kustomization to suspend
  flux_kustomization_namespace: "" # namespace of Flux Kustomization
```

All endpoints are derived from `operator_name` (unless `local: true`):

- Milvus gRPC: `{operator_name}-milvus:19530` → `localhost:19530` when local
- Milvus Management HTTP: `http://{operator_name}-milvus:9091` → `http://localhost:9091` when local (derived automatically, no config field)
- Etcd: `{operator_name}-etcd:2379` → `localhost:2379` when local
- Milvus CR: `kind: Milvus / name: {operator_name} namespace: {namespace}`

## Usage

```shell
make run CMD="snapshot create"
make run CMD="snapshot list"
make run CMD="snapshot restore"
make envs
```

## Snapshot Create

`snapshot create` performs a point-in-time backup of all Milvus data (etcd metadata + S3 segment data). The snapshot preserves pre-built indexes, avoiding costly reindexing on restore.

**Flow:**

1. **Deny writing** on all databases — clean cutoff, reads still served
2. **Pause GC** — prevents S3 object deletion during snapshot (non-fatal if it fails)
3. **Flush all** — persists in-memory segments to S3
4. **Snapshot etcd** — streams via Maintenance API, uploads to `s3://{backup_bucket}/{backup_etcd_path}/{snapshot_id}.db`
5. **Copy S3 data** — parallel server-side copy to `s3://{backup_bucket}/{backup_s3_path}/{snapshot_id}/`
6. **Resume GC** + **Allow writing** — always runs via defers, even on error

Snapshot ID is a UTC timestamp: `2006-01-02T15-04-05Z`.

## Snapshot List

`snapshot list` lists the 3 most recent Milvus snapshots stored in S3.

For each snapshot ID it checks whether both components are present:

- **Etcd**: `s3://{backup_bucket}/{backup_etcd_path}/{snapshot_id}.db`
- **S3 Data**: `s3://{backup_bucket}/{backup_s3_path}/{snapshot_id}/`

Output format:

```
────────────────────────────────────────────────────────────
  Snapshot : 2025-04-29T10-00-00Z
  Status   : complete
  Etcd     : s3://milvus-backup/etcd-snapshots/2025-04-29T10-00-00Z.db
  S3 Data  : s3://milvus-backup/s3-snapshots/2025-04-29T10-00-00Z/
```

Status is `complete` when both components are present, `incomplete` otherwise.

## Snapshot Restore

`snapshot restore` performs a full disaster recovery of Milvus from a raw snapshot. It uses a teardown + recreate approach with least possible downtime.

**Prerequisites:**

- Milvus managed by [milvus-operator](https://github.com/milvus-io/milvus-operator) with Bitnami etcd (inCluster)
- Flux Kustomization managing Milvus resources
- EBS-backed etcd PVCs (RWO)
- Service account (configured via `restore.job_service_account`) with S3 read access to backup bucket

**Usage:**

```shell
# Restore from latest complete snapshot with CLI (interactive configuration/confirmation gates)
make run CMD="snapshot restore"

# Restore a specific snapshot without confirmations (non-interactive overrides as flags)
make run CMD="snapshot restore --snapshot-id 2025-04-29T10-00-00Z --force"
```

**Flow:**

1. **Resolve snapshot** — Latest complete, `--snapshot-id` flag, shapshot_id from the config file or SNAPSHOT_ID from envs.s
2. **Confirm snapshot** — User confirms snapshot ID [Gate 1]
3. **Suspend Flux** — Patch the configured flux kustomization(`restore.`) with `.spec.suspend: true`
4. **Delete scalers** — Delete all HPAs + KEDA ScaledObjects in the configured namespace(`milvus.namespace`).
5. **Delete Milvus CR** — Confirm Milvus CR name and namespace [Gate 2]; After explicit consent, delete the Milvus CR and then the operator tears down all.
6. **Wait** — Wait for all pods in the namespace to terminate
7. **Delete etcd PVCs** — stale data removed
8. **Delete S3** — wipe root bucket/path [Gate 3]
9. **Copy S3** — server-side copy from backup [Gate 4]
10. **Seed etcd** — temp PVC + Job downloads snapshot from S3
11. **Recreate CR** — etcd `replicaCount: 1` + Bitnami `startFromSnapshot`
12. **Wait etcd-0** — single replica restores from snapshot
13. **Scale etcd** — patch CR to original replica count
14. **Wait healthy** — all etcd members + Milvus components
15. **Resume Flux** — reconciles CR to Git state, recreates scalers
16. **Cleanup** — delete temp PVC + Job

**Key design:**

- Uses Bitnami etcd chart's official `startFromSnapshot` mechanism
- Single-replica bootstrap avoids EBS RWO limitation
- Live CR is captured before deletion and reused for recreation
- Flux handles final state reconciliation (removes temp config, recreates scalers)
