# Project Brief — milvus-utils

## Goal

Build a production-ready CLI tool that creates and restores **raw S3 + etcd snapshots** for Milvus 2.5.x+ running on EKS, deployed as a Kubernetes CronJob.

## Problem Statement

Milvus's built-in backup tool triggers index rebuilding on restore, which is slow and resource-intensive. Raw snapshots bypass this by capturing etcd metadata and S3 segment data as-is, preserving pre-built indexes.

## Core Capabilities

### Snapshot Create

1. Connect to Milvus gRPC, enumerate all databases.
2. Set `database.force.deny.writing = true` on all DBs (deferred restore to `false`).
3. Pause GC via management HTTP API (prevents S3 object deletion during snapshot).
4. Flush all collections in each database.
5. Create etcd snapshot via Maintenance API → upload to backup S3 bucket.
6. Server-side copy of Milvus S3 data to backup bucket.
7. Resume GC (with ticket from step 3).
8. Restore write access (deferred, runs even on error).

### Snapshot Restore

1. Disable Flux reconciliation on the Milvus operator CR.
2. Scale etcd StatefulSet to 0.
3. Delete etcd PVCs (so fresh PVCs pick up restored data).
4. Download etcd snapshot from backup bucket and seed fresh etcd.
5. Server-side copy of S3 data from backup → production bucket.
6. Scale etcd StatefulSet back up (wait for ready).
7. Re-enable Flux — operator reconciles and restarts Milvus components.

### Snapshot List

- Lists available snapshots from the backup bucket with metadata.

## Constraints

- Runs **inside** EKS pod; credential chain is IRSA (no explicit creds).
- Namespace is implicit (uses in-cluster service account namespace).
- All endpoints derived from `milvus.operator_name` config key, unless `milvus.local: true` in which case `localhost` is used (useful for local development/testing).
- No K8s scaling during create — only write quiescing via Milvus API.
- CLI built with Cobra + Viper; config from YAML + env vars.
