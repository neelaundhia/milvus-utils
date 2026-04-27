package cmd

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/neelaundhia/milvus-utils/internal/etcd"
	"github.com/neelaundhia/milvus-utils/internal/milvus"
	"github.com/neelaundhia/milvus-utils/internal/s3"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	// snapshotIDFormat is the Go time format used to generate snapshot IDs.
	snapshotIDFormat = "2006-01-02T15-04-05Z"
	// gcPauseSeconds is the GC pause TTL sent to Milvus. The server re-enables
	// GC automatically after this many seconds if we never call ResumeGC.
	gcPauseSeconds = 3600
)

// createCmd represents the create command.
var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Milvus snapshot",
	Long: `Create a point-in-time snapshot of Milvus data (etcd + S3).

The command quiesces writes, flushes in-memory segments, snapshots etcd,
copies S3 data server-side to the backup bucket, then re-enables writes.
Reads continue to be served throughout.`,
	RunE: runCreate,
}

func init() { //nolint:gochecknoinits
	snapshotCmd.AddCommand(createCmd)
}

func runCreate(cmd *cobra.Command, _ []string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log := GetLogger(cfg.Log)
	ctx := cmd.Context()

	snapshotID := time.Now().UTC().Format(snapshotIDFormat)
	log.WithField("snapshot_id", snapshotID).Info("starting snapshot create")

	// ── Initialise clients ──────────────────────────────────────────────
	milvusClient, err := milvus.NewClient(ctx, cfg.Milvus.GRPCAddr(), cfg.Milvus.Username, cfg.Milvus.Password)
	if err != nil {
		return err
	}
	defer milvusClient.Close(ctx)

	etcdClient, err := etcd.NewClient(ctx, cfg.Milvus.EtcdEndpoints())
	if err != nil {
		return err
	}
	defer etcdClient.Close()

	s3Opts := []s3.Option{}
	if cfg.AWS.Region != "" {
		s3Opts = append(s3Opts, s3.WithRegion(cfg.AWS.Region))
	}
	if cfg.AWS.Endpoint != "" {
		s3Opts = append(s3Opts, s3.WithEndpoint(cfg.AWS.Endpoint))
	}
	s3Client, err := s3.NewClient(ctx, s3Opts...)
	if err != nil {
		return err
	}

	// ── Step 1: Deny writing on all databases ───────────────────────────
	dbs, err := milvusClient.ListDatabases(ctx)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}
	for _, db := range dbs {
		log.WithField("db", db).Info("denying writes")
		if err := milvusClient.SetDenyWriting(ctx, db, true); err != nil {
			return err
		}
	}
	// Always re-enable writes, even on error.
	defer func() {
		for _, db := range dbs {
			log.WithField("db", db).Info("allowing writes")
			if err := milvusClient.SetDenyWriting(context.Background(), db, false); err != nil {
				log.WithError(err).WithField("db", db).Error("failed to allow writes (manual intervention required)")
			}
		}
	}()

	// ── Step 2: Pause GC ────────────────────────────────────────────────
	ticket, err := milvusClient.PauseGC(ctx, gcPauseSeconds)
	if err != nil {
		// Non-fatal: GC runs infrequently, short snapshots are safe without it.
		log.WithError(err).Warn("failed to pause GC, continuing snapshot")
	} else {
		log.WithField("ticket", ticket).Info("GC paused")
		defer func() {
			if err := milvusClient.ResumeGC(context.Background(), ticket); err != nil {
				log.WithError(err).Error("failed to resume GC (will auto-resume after TTL)")
			} else {
				log.Info("GC resumed")
			}
		}()
	}

	// ── Step 3: Flush all databases ─────────────────────────────────────
	log.Info("flushing all databases")
	if err := milvusClient.FlushAll(ctx); err != nil {
		return fmt.Errorf("flushing: %w", err)
	}
	log.Info("flush complete")

	// ── Step 4a: Snapshot etcd ──────────────────────────────────────────
	log.Info("taking etcd snapshot")
	var etcdBuf bytes.Buffer
	if err := etcdClient.Snapshot(ctx, &etcdBuf); err != nil {
		return fmt.Errorf("etcd snapshot: %w", err)
	}
	log.WithField("size_bytes", etcdBuf.Len()).Info("etcd snapshot captured")

	// ── Step 4b: Upload etcd snapshot to S3 ─────────────────────────────
	backupBucket := s3.ParseBucketURI(cfg.Milvus.BackupBucket)
	etcdKey := cfg.Milvus.BackupEtcdPath + "/" + snapshotID + ".snapshot"
	if err := s3Client.Upload(ctx, backupBucket, etcdKey, &etcdBuf); err != nil {
		return fmt.Errorf("uploading etcd snapshot: %w", err)
	}
	log.WithField("key", etcdKey).Info("etcd snapshot uploaded to S3")

	// ── Step 4c: Copy S3 data (server-side) ─────────────────────────────
	rootBucket := s3.ParseBucketURI(cfg.Milvus.RootBucket)
	srcPrefix := cfg.Milvus.RootPath + "/"
	dstPrefix := cfg.Milvus.BackupS3Path + "/" + snapshotID + "/"
	copied, err := s3Client.CopyPrefix(ctx, rootBucket, srcPrefix, backupBucket, dstPrefix)
	if err != nil {
		return fmt.Errorf("copying S3 data: %w", err)
	}
	log.WithFields(logrus.Fields{"objects": copied, "dst": fmt.Sprintf("s3://%s/%s", backupBucket, dstPrefix)}).Info("S3 copy complete")

	// ── Done ────────────────────────────────────────────────────────────
	// GC resumed + writes re-enabled by defers above.
	log.WithField("snapshot_id", snapshotID).Info("snapshot create complete")
	return nil
}
