# Technical Context — milvus-utils

## Language & Tooling

- **Go 1.25** (CI enforced via `github.com/neelaundhia/workflows`)
- **Dev environment:** VS Code dev container (`mcr.microsoft.com/devcontainers/go:2-1.25-trixie`)
- Go commands run natively inside the dev container — no Podman wrapper needed

## Key Dependencies

| Package                                        | Purpose                          |
| ---------------------------------------------- | -------------------------------- |
| `github.com/spf13/cobra`                       | CLI framework                    |
| `github.com/spf13/viper`                       | Config loading (YAML + env vars) |
| `github.com/sirupsen/logrus`                   | Structured logging               |
| `github.com/milvus-io/milvus/client/v2 v2.6.3` | Milvus gRPC SDK                  |
| `go.etcd.io/etcd/client/v3 v3.5.5`             | Etcd Maintenance API             |

### Planned additions by phase

| Phase | Package                        | Purpose           |
| ----- | ------------------------------ | ----------------- |
| 4     | `github.com/aws/aws-sdk-go-v2` | S3 operations     |
| 6     | `k8s.io/client-go`             | Kubernetes client |

## Configuration

Config is loaded in layers (later layers override earlier):

1. `config.yaml` (from `.` or `/config`)
2. `secrets.yaml` (merged if present)
3. `--config <file>` flag (merged if provided)
4. Environment variables (auto-mapped, `.` → `_`)

### Config Structure

```yaml
log:
  level: debug # debug|info|warn|error|fatal|panic
  format: json # json|text

milvus:
  local: false           # if true, all endpoints use localhost (ignores operator_name/namespace)
  operator_name: "milvus" # drives all derived endpoints (ignored when local: true)
  root_bucket: "s3://milvus" # production Milvus data bucket
  root_path: "files" # S3 prefix within root_bucket
  backup_bucket: "s3://milvus-backup"
  backup_etcd_path: "etcd-snapshots"
  backup_s3_path: "s3-snapshots"
```

### Derived Endpoints

`MilvusConfig` exposes helper methods that return the correct address based on `Local`:

| Method | local: false | local: true |
|---|---|---|
| `GRPCAddr()` | `{operator_name}-milvus:19530` | `localhost:19530` |
| `EtcdEndpoints()` | `[{operator_name}-etcd:2379]` | `[localhost:2379]` |
| Mgmt HTTP (derived in `NewClient`) | `http://{operator_name}-milvus:9091` | `http://localhost:9091` |

```
Milvus CR:  milvus.io/v1beta1 / Kind: Milvus / name: {operator_name}
Etcd STS:   {operator_name}-etcd
```

Always use `cfg.Milvus.GRPCAddr()` and `cfg.Milvus.EtcdEndpoints()` — never build addresses inline.

## Kubernetes Deployment

- Runs as a **CronJob** in EKS
- **IRSA** for S3 access (AWS credentials from service account annotation)
- In-cluster K8s client (uses pod service account)
- RBAC for: Milvus CR (patch), etcd STS (patch/scale), etcd PVCs (delete/list)
- Flux annotation: `kustomize.toolkit.fluxcd.io/reconcile: disabled`

## Milvus Internals

- Milvus 2.5.x stores segment metadata in **etcd** and segment data in **S3**
- etcd snapshot + S3 copy = complete, index-preserving backup
- `database.force.deny.writing` / `database.force.deny.reading` via `AlterDatabaseProperties` API
- GC pause/resume via management HTTP API (port 9091, not gRPC)
  - PauseGC returns a ticket; ResumeGC requires that ticket
  - Management URL derived from gRPC host + `:9091` inside `NewClient`
- Compaction does not need separate pausing (deny-writing prevents new compactions; compaction only creates files, GC deletes them)

## Build & Run

Run inside the dev container (or any host with Go 1.25):

```bash
make build                        # go build
make test                         # go test ./...
make tidy                         # go mod tidy
make run CMD="snapshot create"    # run subcommand
make envs                         # print config env vars
```
