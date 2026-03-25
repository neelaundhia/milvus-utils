package cmd

import "github.com/spf13/cobra"

// createCmd represents the create command.
var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a Milvus snapshot",
	Long:  "Create a Milvus snapshot.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

func init() { //nolint:gochecknoinits
	snapshotCmd.AddCommand(createCmd)
}
