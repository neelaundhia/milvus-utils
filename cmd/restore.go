package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/neelaundhia/milvus-utils/internal/k8s"
	"github.com/neelaundhia/milvus-utils/internal/s3"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	restoreJobTimeout = 10 * time.Minute
	restoreWaitTimeout = 10 * time.Minute
	etcdReadyTimeout  = 10 * time.Minute
)

var (
	restoreForce      bool
	restoreSnapshotID string
)

// restoreCmd represents the restore command.
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore a Milvus snapshot",
	Long: `Restore Milvus from a raw S3 + etcd snapshot.

The command suspends Flux, tears down Milvus, restores S3 data and etcd,
then recreates the Milvus CR with startFromSnapshot configuration.
After etcd bootstraps, Flux is resumed to reconcile to Git state.`,
	RunE: runRestore,
}

func init() { //nolint:gochecknoinits
	restoreCmd.Flags().BoolVar(&restoreForce, "force", false, "skip confirmation prompts (for non-interactive use)")
	restoreCmd.Flags().StringVar(&restoreSnapshotID, "snapshot-id", "", "snapshot ID to restore (default: latest complete)")
	snapshotCmd.AddCommand(restoreCmd)
}

func runRestore(cmd *cobra.Command, _ []string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log := GetLogger(cfg.Log)
	ctx := cmd.Context()

	// ── Resolve snapshot ID ─────────────────────────────────────────────
	snapshotID := restoreSnapshotID
	if snapshotID == "" {
		snapshotID = cfg.Restore.SnapshotID
	}
	if snapshotID == "" {
		snapshotID = os.Getenv("SNAPSHOT_ID")
	}

	// Initialise S3 client for snapshot resolution and data restore.
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

	// If no snapshot ID provided, find the latest complete one.
	if snapshotID == "" {
		log.Info("no snapshot ID specified, resolving latest complete snapshot")
		resolved, err := resolveLatestSnapshot(ctx, s3Client, cfg, log)
		if err != nil {
			return err
		}
		snapshotID = resolved
	}
	log.WithField("snapshot_id", snapshotID).Info("resolved snapshot for restore")

	// ── Gate 1: Confirm snapshot ────────────────────────────────────────
	if !restoreForce {
		if !confirm(fmt.Sprintf("Restore from snapshot %q?", snapshotID)) {
			return fmt.Errorf("restore cancelled by user")
		}
	}

	// ── Initialise K8s client ───────────────────────────────────────────
	k8sClient, err := k8s.NewClient(log)
	if err != nil {
		return err
	}

	namespace := cfg.Milvus.Namespace
	operatorName := cfg.Milvus.OperatorName

	// ── Step 1: Suspend Flux ────────────────────────────────────────────
	if cfg.Restore.FluxKustomizationName != "" {
		log.Info("suspending flux kustomization")
		if err := k8sClient.SuspendFlux(ctx, cfg.Restore.FluxKustomizationName, cfg.Restore.FluxKustomizationNamespace); err != nil {
			return err
		}
	}

	// ── Gate 2: Confirm destructive actions ─────────────────────────────
	if !restoreForce {
		msg := fmt.Sprintf("About to perform DESTRUCTIVE operations on Milvus CR %q in namespace %q. Continue?", operatorName, namespace)
		if !confirm(msg) {
			return fmt.Errorf("restore cancelled by user")
		}
	}

	// ── Step 3: Delete scalers ──────────────────────────────────────────
	log.Info("deleting HPAs")
	if err := k8sClient.DeleteHPAs(ctx, namespace); err != nil {
		return err
	}
	log.Info("deleting KEDA ScaledObjects")
	if err := k8sClient.DeleteScaledObjects(ctx, namespace); err != nil {
		return err
	}

	// ── Step 4: Scale down Milvus workers ───────────────────────────────
	log.Info("scaling down Milvus workers via CR")
	if err := k8sClient.ScaleDownMilvus(ctx, operatorName, namespace); err != nil {
		return err
	}

	// ── Step 5: Delete etcd Helm release, PVCs and PVs ─────────────────
	etcdReleaseName := operatorName + "-etcd"
	etcdLabelSelector := fmt.Sprintf("app.kubernetes.io/instance=%s-etcd,app.kubernetes.io/name=etcd", operatorName)
	log.Info("deleting etcd resources (Helm release, PVCs, PVs)")
	if err := k8sClient.DeleteEtcdResources(ctx, namespace, etcdReleaseName, etcdLabelSelector); err != nil {
		return err
	}

	// ── Step 6: Delete S3 files ─────────────────────────────────────────
	log.Info("deleting Milvus S3 data")
	rootBucket := s3.ParseBucketURI(cfg.Milvus.RootBucket)
	rootPrefix := cfg.Milvus.RootPath + "/"
	deleted, err := s3Client.DeletePrefix(ctx, rootBucket, rootPrefix)
	if err != nil {
		return fmt.Errorf("deleting S3 data: %w", err)
	}
	log.WithField("objects", deleted).Info("S3 data deleted")

	// ── Step 7: Wait for all pods to terminate ──────────────────────────
	podSelector := fmt.Sprintf("app.kubernetes.io/instance=%s", operatorName)
	log.Info("waiting for all Milvus pods to terminate")
	if err := k8sClient.WaitForPodsTerminated(ctx, namespace, podSelector, restoreWaitTimeout); err != nil {
		return err
	}

	// ── Step 8: Copy S3 data from snapshot ──────────────────────────────
	log.Info("restoring S3 data from snapshot")
	srcPrefix := cfg.Milvus.BackupS3Path + "/" + snapshotID + "/"
	copied, err := s3Client.CopyPrefix(ctx, backupBucket, srcPrefix, rootBucket, rootPrefix)
	if err != nil {
		return fmt.Errorf("copying S3 data from snapshot: %w", err)
	}
	log.WithFields(logrus.Fields{"objects": copied}).Info("S3 data restored")

	// ── Step 9: Seed etcd (temp PVC + download Job) ─────────────────────
	safeID := sanitizeK8sName(snapshotID)
	pvcName := fmt.Sprintf("milvus-restore-snapshot-%s", safeID)
	jobName := fmt.Sprintf("milvus-restore-download-%s", safeID)
	etcdS3URI := fmt.Sprintf("s3://%s/%s/%s.db", backupBucket, cfg.Milvus.BackupEtcdPath, snapshotID)

	log.Info("creating temp PVC for etcd snapshot")
	if err := k8sClient.CreateTempPVC(ctx, namespace, pvcName, cfg.Restore.StorageClass); err != nil {
		return err
	}

	log.Info("creating download job")
	if err := k8sClient.CreateDownloadJob(ctx, namespace, jobName, pvcName,
		cfg.Restore.JobServiceAccount, cfg.Restore.JobImage,
		etcdS3URI, "/snapshot/snapshot.db"); err != nil {
		return err
	}

	log.Info("waiting for download job to complete")
	if err := k8sClient.WaitForJobComplete(ctx, namespace, jobName, restoreJobTimeout); err != nil {
		return err
	}

	// ── Step 10: Patch Milvus CR with startFromSnapshot ─────────────────
	log.Info("patching Milvus CR with startFromSnapshot config")
	startFromSnapshotPatch := buildStartFromSnapshotPatch(pvcName)
	if err := k8sClient.PatchMilvusCR(ctx, operatorName, namespace, startFromSnapshotPatch); err != nil {
		return err
	}

	// ── Step 11: Wait for etcd-0 to be ready ────────────────────────────
	etcdStsName := operatorName + "-etcd"
	log.WithField("statefulset", etcdStsName).Info("waiting for etcd to be ready")
	if err := k8sClient.WaitForStatefulSetReady(ctx, namespace, etcdStsName, etcdReadyTimeout); err != nil {
		return err
	}

	// ── Step 12: Resume Flux ────────────────────────────────────────────
	if cfg.Restore.FluxKustomizationName != "" {
		log.Info("resuming flux kustomization")
		if err := k8sClient.ResumeFlux(ctx, cfg.Restore.FluxKustomizationName, cfg.Restore.FluxKustomizationNamespace); err != nil {
			return err
		}
	}

	// ── Step 13: Cleanup temp resources ─────────────────────────────────
	log.Info("cleaning up temp resources")
	if err := k8sClient.DeleteJob(ctx, namespace, jobName); err != nil {
		log.WithError(err).Warn("failed to delete download job")
	}
	if err := k8sClient.DeletePVC(ctx, namespace, pvcName); err != nil {
		log.WithError(err).Warn("failed to delete temp PVC")
	}

	log.WithField("snapshot_id", snapshotID).Info("snapshot restore complete")
	return nil
}

// resolveLatestSnapshot finds the latest complete snapshot (has both etcd + S3).
func resolveLatestSnapshot(ctx context.Context, s3Client *s3.Client, cfg *Config, log *logrus.Logger) (string, error) {
	backupBucket := s3.ParseBucketURI(cfg.Milvus.BackupBucket)

	// Collect etcd snapshot IDs.
	etcdPrefix := cfg.Milvus.BackupEtcdPath + "/"
	etcdKeys, err := s3Client.ListObjects(ctx, backupBucket, etcdPrefix)
	if err != nil {
		return "", fmt.Errorf("listing etcd snapshots: %w", err)
	}
	etcdIDs := make(map[string]struct{})
	for _, key := range etcdKeys {
		name := strings.TrimPrefix(key, etcdPrefix)
		if strings.HasSuffix(name, ".db") && !strings.Contains(name, "/") {
			etcdIDs[strings.TrimSuffix(name, ".db")] = struct{}{}
		}
	}

	// Collect S3 data snapshot IDs.
	s3DataPrefix := cfg.Milvus.BackupS3Path + "/"
	s3Prefixes, err := s3Client.ListCommonPrefixes(ctx, backupBucket, s3DataPrefix, "/")
	if err != nil {
		return "", fmt.Errorf("listing S3 data snapshots: %w", err)
	}
	s3IDs := make(map[string]struct{})
	for _, p := range s3Prefixes {
		name := strings.TrimPrefix(p, s3DataPrefix)
		id := strings.TrimSuffix(name, "/")
		if id != "" {
			s3IDs[id] = struct{}{}
		}
	}

	// Find complete snapshots (present in both).
	var complete []string
	for id := range etcdIDs {
		if _, ok := s3IDs[id]; ok {
			complete = append(complete, id)
		}
	}
	if len(complete) == 0 {
		return "", fmt.Errorf("no complete snapshots found")
	}

	sort.Sort(sort.Reverse(sort.StringSlice(complete)))
	log.WithField("snapshot_id", complete[0]).Info("latest complete snapshot found")
	return complete[0], nil
}

// confirm prompts the user for y/n confirmation. Returns true if confirmed.
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

// sanitizeK8sName lowercases s and replaces any character that is not a
// lowercase alphanumeric or '-' with '-', making it safe for use in
// Kubernetes resource names (RFC 1123 subdomain).
func sanitizeK8sName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// buildStartFromSnapshotPatch returns a JSON merge patch that configures etcd
// for single-replica snapshot restore via the Bitnami startFromSnapshot mechanism.
func buildStartFromSnapshotPatch(pvcName string) []byte {
	return []byte(fmt.Sprintf(`{
		"spec": {
			"dependencies": {
				"etcd": {
					"inCluster": {
						"values": {
							"replicaCount": 1,
							"startFromSnapshot": {
								"enabled": true,
								"existingClaim": %q,
								"snapshotFilename": "snapshot.db"
							}
						}
					}
				}
			}
		}
	}`, pvcName))
}

