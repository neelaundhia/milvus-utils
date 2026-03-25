I've read through the milvus-backup source code and documentation. Here's what I found:

Yes, milvus-backup requires full index rebuilding on restore
From the coll_ddl_task.go source, the restore flow is:

Create collection (schema only)
Create index definitions (vector + scalar — just the metadata, no pre-built index data)
Create partitions
BulkInsert all raw data (insert logs + delta logs)
The backup only stores raw binlog data (insert logs and delta logs), not the pre-built index files. On restore, the tool creates the index definitions first, then bulk-inserts the raw data. Milvus then builds all indexes from scratch on the imported segments. This is why your DataNodes go red — they're processing the BulkInsert jobs and rebuilding all indexes in the background. This is expected behavior, not an error.

GC pause is for backup, not restore
The gcPause setting in backup.yaml only applies to the backup phase:

Its purpose is to prevent Milvus from garbage-collecting segments while the backup tool is reading them. Without it, GC could delete segments mid-backup, causing inconsistent or failed backups (e.g., the field xxx has different file num to other fields error).

Not pausing GC won't affect your restore/reindexing time — but you should enable it during backups to avoid backup integrity issues.

Summary
The long DataNode activity during restore is the expected index rebuild, not a misconfiguration.
gcPause has no effect on restore or indexing duration. Enable it for backups though.
To speed up restores, consider the --use_v2_restore flag (merges segments into fewer BulkInsert jobs — up to 10x faster in their internal testing, requires Milvus >= 2.4).
