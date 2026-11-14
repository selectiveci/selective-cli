package main

import (
	"io"
	"os"
	"strings"

	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/update"
	"github.com/spf13/cobra"
)

var (
	formatIsTerminal = format.IsTerminal
	isTerminal       = formatIsTerminal
)

var noNoticeCommands = map[string]bool{"version": true, "help": true, "completion": true, "mcp": true}

func skipsNotice(cmd *cobra.Command) bool {
	if cmd == nil || !cmd.Runnable() || cmd.Annotations["update_notice"] == "skip" {
		return true
	}
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil && helpFlag.Changed {
		return true
	}
	path := strings.Fields(cmd.CommandPath())
	return len(path) > 1 && noNoticeCommands[path[1]]
}

func isVersionCommand(root *cobra.Command, args []string) bool {
	cmd, _, err := root.Find(args)
	return err == nil && cmd.Parent() == root && cmd.Name() == "version"
}

func printUpdateNotice(cmd *cobra.Command, stdout, stderr io.Writer) {
	if skipsNotice(cmd) || !update.Enabled(os.Getenv) || !isTerminal(stdout) || !isTerminal(stderr) {
		return
	}
	_, _ = io.WriteString(stderr, update.Notice(update.Status(version, commit, update.LoadState())))
}
