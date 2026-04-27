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
│   ├── create.go        # `snapshot create`
│   └── restore.go       # `snapshot restore`
├── internal/
│   └── milvus/
│       ├── client.go     # Milvus gRPC SDK client (Flush, SetDenyWriting, etc.)
│       └── management.go # Milvus management HTTP client (PauseGC, ResumeGC)

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
make run CMD="snapshot restore"
make envs
```

## CLI

Built with [cobra](https://github.com/spf13/cobra). To scaffold a new subcommand:

```shell
cobra add mycommand
```
