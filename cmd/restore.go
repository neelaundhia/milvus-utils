package cmd

import "github.com/spf13/cobra"

// restoreCmd represents the restore command.
var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore a Milvus snapshot",
	Long:  "Restore a Milvus snapshot.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() { //nolint:gochecknoinits
	snapshotCmd.AddCommand(restoreCmd)
}
