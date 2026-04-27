# System Patterns — milvus-utils

## CLI Structure (Cobra)

```
milvus-utils
├── snapshot
│   ├── create    — quiesce + etcd snapshot + S3 copy
│   ├── restore   — etcd restore + S3 restore via K8s
│   └── list      — list available snapshots
└── envs          — print resolved config env var keys
```

Each subcommand lives in its own file under `cmd/`.

## Config Pattern (Viper)

- `Config` struct in `cmd/root.go` with `mapstructure` tags
- `setDefaults()` walks struct and registers `default` struct tags with viper
- `bindEnvs()` walks struct and binds env vars (e.g. `MILVUS_OPERATOR_NAME`)
- `loadConfig()` unmarshals viper state into `Config` struct
- Secrets overlay: `secrets.yaml` is merged after `config.yaml`

## Error Handling Pattern

- `RunE` functions return errors (Cobra prints them)
- Deferred calls used for cleanup (e.g., restoring `deny.writing = false`)
- Logrus for structured logging at each step

## Internal Package Pattern

Each `internal/` package follows:

```go
// client.go
type Client struct { ... }

func NewClient(ctx context.Context, ...) (*Client, error) { ... }
func (c *Client) Close() { ... }
// domain methods...
```

Interfaces are defined alongside implementations to enable test mocking.

Within a package, methods may be split across multiple files by concern, e.g.:

```
internal/milvus/
  client.go      — struct definition, NewClient, gRPC SDK methods
  management.go  — HTTP management API methods (PauseGC, ResumeGC)
```

## S3 Naming Conventions

```
Etcd snapshot:  s3://{backup_bucket}/{backup_etcd_path}/{snapshot_id}.snapshot
S3 snapshot:    s3://{backup_bucket}/{backup_s3_path}/{snapshot_id}/
Production data: s3://{root_bucket}/{root_path}/
```

Snapshot IDs are timestamp strings: `2006-01-02T15-04-05Z` (Go time format).

## Snapshot Create Write-Quiesce Window

Write blocking occurs only during these steps:

1. Set `deny.writing = true` — clean cutoff, no new data enters
2. ← **write-blocked window starts** →
3. Pause GC — prevents S3 object deletion during snapshot
4. Flush all — persists in-memory segments to S3
5. Create etcd snapshot
6. Copy S3 data (server-side, no pod I/O)
7. Resume GC (with ticket from step 3)
8. ← **write-blocked window ends** →
9. Set `deny.writing = false` (deferred, always runs)

Deny writing must come *before* flush to prevent new writes sneaking in between flush and snapshot. GC is paused before flush to eliminate any edge case where an in-flight compaction marks segments as dropped during flush. Reads are served throughout. Defer ensures writes are always re-enabled.

## GC Pause Pattern

Milvus GC is paused/resumed via the **management HTTP API** (port 9091), not the gRPC SDK:

| Operation | Endpoint                                                               | Returns              |
| --------- | ---------------------------------------------------------------------- | -------------------- |
| Pause GC  | `GET /management/datacoord/garbage_collection/pause?pause_seconds=<N>` | `{"msg":"OK","ticket":"..."}` |
| Resume GC | `GET /management/datacoord/garbage_collection/resume?ticket=<ticket>`  | `{"msg":"OK"}`          |

The management URL is derived automatically inside `internal/milvus.NewClient` — it takes the host from the gRPC `addr` and uses port `9091`. No extra config field is needed.

### GC Ticket Mechanism

PauseGC returns an opaque **ticket** (base64-encoded JSON containing a UUID token + collection_id). This ticket **must** be passed to ResumeGC. Without it, the server tries to parse an empty `collection_id` and returns HTTP 500 (`strconv.ParseInt: parsing "": invalid syntax`).

**API signatures:**
- `PauseGC(ctx, pauseSeconds) (ticket string, err error)`
- `ResumeGC(ctx, ticket string) error`

Notes (confirmed from milvus-backup and Milvus proxy source):

- Pause requires a TTL (`pause_seconds`); the server re-enables GC when it expires.
- For long snapshots the lease must be renewed before expiry (every `pause_seconds/2`).
- Pause failure is non-fatal (GC runs infrequently; short snapshots are safe without it).
- Uses only stdlib `net/http` — no new dependency.
- Compaction does **not** need to be paused separately — deny-writing prevents new compactions, and compaction only creates new files (GC deletes old ones).

## Restore Orchestration Pattern

```
Disable Flux → Scale etcd to 0 → Delete PVCs → Restore data →
Scale etcd up → Wait ready → Re-enable Flux → Wait Milvus healthy
```

The Milvus operator handles component restart when Flux re-reconciles.

## Open Questions (to resolve in Phase 7)

- Exact mechanism to seed fresh etcd with snapshot:
  - (a) Init container that downloads + loads snapshot before etcd starts
  - (b) `etcdctl snapshot restore` into new PVC
  - (c) Temporarily patch etcd STS with init container
