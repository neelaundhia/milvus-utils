# milvus-utils

CLI tool for managing Milvus vector-database instances in Kubernetes. Core operations: snapshot Milvus data (S3 + etcd) and restore it, pausing/resuming dependent Kubernetes workloads around those operations via JSON Patch.

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
│   │   ├── client.go     # Milvus gRPC SDK client (Flush, SetDenyWriting, etc.)
│   │   └── management.go # Milvus management HTTP client (PauseGC, ResumeGC)
│   ├── etcd/
│   │   └── client.go     # Etcd Maintenance API client (Snapshot)
│   └── s3/
│       ├── client.go     # AWS S3 client (List, Upload, Download)
│       └── parallel.go   # Parallel server-side copy & batch delete
```

## Build and Test

All Go commands run inside a Podman container. Use the Makefile targets:

```shell
make build          # go build -o /tmp/milvus-utils main.go
make test           # go test ./...
make tidy           # go mod tidy
make run CMD="..."  # go run main.go <args>
make envs           # inspect available config environment variables
make clean          # remove Podman volume caches
```

## Configuration

Copy `config.example.yaml` to `config.yaml` and fill in your values. Secrets (credentials) go in `secrets.yaml` (gitignored). Config is also configurable via environment variables (e.g. `MILVUS_OPERATOR_NAME`).

```yaml
log:
  level: info # debug|info|warn|error
  format: json # json|text

aws:
  region: "eu-west-1"   # AWS region for S3 buckets
  endpoint: ""           # optional: override S3 endpoint (e.g. LocalStack)

milvus:
  local: false             # if true, use localhost for all endpoints (ignores operator_name/namespace)
  operator_name: "milvus" # derives all endpoints (gRPC, etcd, K8s CR)
  root_bucket: "s3://milvus" # production Milvus data bucket
  root_path: "files" # S3 prefix within root_bucket
  backup_bucket: "s3://milvus-backup"
  backup_etcd_path: "etcd-snapshots"
  backup_s3_path: "s3-snapshots"
```

All endpoints are derived from `operator_name` (unless `local: true`):

- Milvus gRPC: `{operator_name}-milvus:19530` → `localhost:19530` when local
- Milvus Management HTTP: `http://{operator_name}-milvus:9091` → `http://localhost:9091` when local (derived automatically, no config field)
- Etcd: `{operator_name}-etcd:2379` → `localhost:2379` when local
- Milvus CR: `Kind Milvus / name {operator_name}`

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

## CLI

Built with [cobra](https://github.com/spf13/cobra). To scaffold a new subcommand:

```shell
cobra add mycommand
```
