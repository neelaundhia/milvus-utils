package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neelaundhia/milvus-utils/internal/s3"
	"github.com/spf13/cobra"
)

const maxListSnapshots = 3

// listCmd represents the list command.
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List available Milvus snapshots",
	Long: `List the 3 most recent Milvus snapshots stored in S3.

Each snapshot consists of an etcd backup (.db) and an S3 data copy.
The command verifies both components are present and prints their S3 paths.`,
	RunE: runList,
}

func init() { //nolint:gochecknoinits
	snapshotCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, _ []string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	ctx := cmd.Context()

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

	backupBucket := s3.ParseBucketURI(cfg.Milvus.BackupBucket)

	// ── Collect etcd snapshot IDs ────────────────────────────────────────
	// Keys have the form: {backup_etcd_path}/{snapshot_id}.db
	etcdPrefix := cfg.Milvus.BackupEtcdPath + "/"
	etcdKeys, err := s3Client.ListObjects(ctx, backupBucket, etcdPrefix)
	if err != nil {
		return fmt.Errorf("listing etcd snapshots: %w", err)
	}
	etcdByID := make(map[string]string) // snapshot_id → full s3 URI
	for _, key := range etcdKeys {
		name := strings.TrimPrefix(key, etcdPrefix)
		// Skip nested keys and non-.db files.
		if !strings.HasSuffix(name, ".db") || strings.Contains(name, "/") {
			continue
		}
		id := strings.TrimSuffix(name, ".db")
		etcdByID[id] = fmt.Sprintf("s3://%s/%s", backupBucket, key)
	}

	// ── Collect S3 data snapshot IDs ─────────────────────────────────────
	// Prefixes have the form: {backup_s3_path}/{snapshot_id}/
	s3DataPrefix := cfg.Milvus.BackupS3Path + "/"
	s3Prefixes, err := s3Client.ListCommonPrefixes(ctx, backupBucket, s3DataPrefix, "/")
	if err != nil {
		return fmt.Errorf("listing S3 data snapshots: %w", err)
	}
	s3ByID := make(map[string]string) // snapshot_id → full s3 URI
	for _, p := range s3Prefixes {
		name := strings.TrimPrefix(p, s3DataPrefix)
		id := strings.TrimSuffix(name, "/")
		if id == "" {
			continue
		}
		s3ByID[id] = fmt.Sprintf("s3://%s/%s", backupBucket, p)
	}

	// ── Union all snapshot IDs ───────────────────────────────────────────
	seen := make(map[string]struct{})
	for id := range etcdByID {
		seen[id] = struct{}{}
	}
	for id := range s3ByID {
		seen[id] = struct{}{}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	// Snapshot IDs are UTC timestamps (2006-01-02T15-04-05Z) which are
	// lexicographically sortable — newest first.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) > maxListSnapshots {
		ids = ids[:maxListSnapshots]
	}

	// ── Print results ────────────────────────────────────────────────────
	if len(ids) == 0 {
		fmt.Println("no snapshots found")
		return nil
	}

	sep := strings.Repeat("─", 60)
	for i, id := range ids {
		etcdPath, hasEtcd := etcdByID[id]
		s3Path, hasS3 := s3ByID[id]
		status := "complete"
		if !hasEtcd || !hasS3 {
			status = "incomplete"
		}
		if !hasEtcd {
			etcdPath = "(missing)"
		}
		if !hasS3 {
			s3Path = "(missing)"
		}
		fmt.Println(sep)
		fmt.Printf("  Snapshot : %s\n", id)
		fmt.Printf("  Status   : %s\n", status)
		fmt.Printf("  Etcd     : %s\n", etcdPath)
		fmt.Printf("  S3 Data  : %s\n", s3Path)
		if i < len(ids)-1 {
			fmt.Println()
		}
	}
	fmt.Println(sep)
	return nil
}
