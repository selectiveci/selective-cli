package main

import (
	"fmt"
	"io"

	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/update"
	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:         "version",
		Short:       "Print version information",
		Annotations: map[string]string{"exit_codes": "0"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			info := update.Status(version, commit, update.LoadState())
			if format.JSONMode(cmd.Flags(), out) {
				return format.Resource(out, "version", info)
			}
			_, err := io.WriteString(out, plainVersion(info))
			return err
		},
	}
}

func plainVersion(info update.Info) string {
	text := fmt.Sprintf("sel %s (%s)\n", info.Version, info.Commit)
	if info.LatestVersion != "" {
		text += fmt.Sprintf("latest: %s\n", info.LatestVersion)
	}
	return text
}
