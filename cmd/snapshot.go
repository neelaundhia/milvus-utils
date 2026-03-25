package cmd

import "github.com/spf13/cobra"

// snapshotCmd represents the snapshot command.
var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage Milvus snapshots",
	Long:  "Manage Milvus snapshots.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() { //nolint:gochecknoinits
	rootCmd.AddCommand(snapshotCmd)
}
