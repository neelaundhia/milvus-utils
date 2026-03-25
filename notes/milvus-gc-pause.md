## Gotcha!

I just found out that we are not pausing GC (Garbage Collection) correctly with milvus-backup. The endpoint for pausing GC is not the GRPC (19530) one but the web management (9091) one. Pausing GC for certain time returns a ticket which has to be used while resuming GC. GC is paused for a certain time duration which can be set in the request.

## References

### Order of Tasks

Deny writing first, then flush. The order matters for consistency.

Why:

If you flush first, there's a race window between flush completing and deny-writing taking effect where new writes can sneak in. Those writes land in in-memory growing segments that aren't persisted to S3, so your snapshot misses them — but etcd may already reference them, causing inconsistency on restore.

Correct order:

Deny writing — creates a clean cutoff, no new data can enter
Flush — persists everything already in memory to S3
Pause GC → snapshot etcd + S3 → resume GC → allow writing
This guarantees that when flush completes, all data is on S3 and nothing new can arrive. The tradeoff is that writes are blocked for the flush duration + snapshot time, but consistency is non-negotiable for a backup.

database.force.deny.writing blocks client operations (insert/delete/upsert) at the proxy layer — it doesn't interfere with flush, which is an internal operation.

### GC VS Compaction

Yes, GC and Compaction are different operations in Milvus, but for our raw S3+etcd snapshot approach, we only need to pause GC — not compaction. Here's why:

Garbage Collection (GC) — Permanently deletes data files (segments) from S3/MinIO that are no longer referenced by metadata. If GC runs mid-snapshot, it could delete S3 objects after our etcd snapshot captured references to them, leaving a broken restore. We must pause GC.

Compaction — Merges small segments into larger ones and rewrites them. It creates new files and updates metadata, but doesn't delete old files immediately (that's GC's job). Since we deny writes (database.force.deny.writing=true) before snapshotting, no new compactions will start. And even if one were in-flight, compaction only creates new files — the old files remain until GC cleans them up. So compaction can't cause data loss during our snapshot window.
