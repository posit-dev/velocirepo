package cmd

import (
	"github.com/posit-dev/velocirepo/internal/selfupdate"
	"github.com/spf13/cobra"
)

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update velocirepo to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			return (&selfupdate.Updater{}).Run(cmd.Context(), cmd.OutOrStdout())
		},
	}
}
