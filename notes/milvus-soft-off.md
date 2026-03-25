# Based on the official Milvus documentation, there are two approaches significantly faster than unloading collections + scaling down workers:

1. **database.force.deny.writing + Flush (for your raw S3/etcd snapshot approach)**  
   From the Milvus database management docs, each database supports these properties:

| Property                    | Type    | Description                                   |
| --------------------------- | ------- | --------------------------------------------- |
| database.force.deny.writing | boolean | Force the database to deny writing operations |
| database.force.deny.reading | boolean | Force the database to deny reading operations |

Your workflow would be:

- **Flush all collections** — this seals all growing (in-memory) segments and persists them as log snapshots to object storage (S3). After flush completes, all data is durable in S3.
- **Set `database.force.deny.writing = true` via AlterDatabaseProperties** — this blocks any new inserts/deletes/upserts at the proxy layer, so no new data flows to S3 or etcd.
- **Take your S3 + etcd snapshots** — data is now quiesced.
- **Set `database.force.deny.writing = false`** — resume normal operations.

This keeps all collections loaded and query nodes serving reads. No worker scale-down, no collection unload/reload. The only disruption is that writes are rejected during the snapshot window.

**Example in Go SDK:**

```go
// ... Go SDK example goes here ...
```

2. **Use milvus-backup instead of raw S3/etcd snapshots**  
   From the milvus-backup README and the Milvus Backup overview docs:

> "The Milvus-backup process has negligible impact on the performance of Milvus. Milvus cluster is fully functional and can operate normally while backup and restoration are in progress."

The tool:

- Flushes collections to persist in-memory data before copying
- Pauses GC (`gcPause.enable: true`, default 7200s) so sealed segments aren't garbage-collected during the copy
- Reads segment metadata via the Milvus API and copies data files directly from object storage to a backup location
- Requires zero downtime — no unload, no scale-down

This operates at the Milvus logical level rather than raw storage snapshots, so it doesn't need S3/etcd snapshot coordination at all.

## Recommendation

If you must stick with raw S3 + etcd snapshots (e.g., for infrastructure-level DR), use approach 1: **Flush + database.force.deny.writing**. It's the fastest path since you avoid the expensive unload/reload cycle and keep query nodes serving reads.

If you're open to changing your backup strategy, **milvus-backup** eliminates the quiesce window entirely and is the officially supported tool for this use case.
