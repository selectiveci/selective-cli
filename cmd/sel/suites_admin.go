package main

import (
	"fmt"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func suitesArchiveCmd() *cobra.Command {
	var f setupFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "archive SUITE",
		Short: "Archive a suite",
		Long: `Archive a suite (by name or ID): it leaves the default suite list and its
flakes are hidden, while its runs and builds stay readable. Needs a token with
the write scope, as an owner or admin of the organization.

Archiving an archived suite changes nothing and exits 0 with result
"unchanged". --dry-run reports what would happen and changes nothing.`,
		Example:     "  sel suites archive web-rspec\n  sel suites archive web-rspec --dry-run --json",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return suitesArchive(env, org, args[0], dryRun)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change without changing it")
	return cmd
}

func suitesArchive(env *setup.Env, org, want string, dryRun bool) error {
	id, err := suiteIDForArchive(env.Client, org, want)
	if err != nil {
		return err
	}
	change, err := env.Client.ArchiveSuite(org, id, dryRun)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Write(env.Stdout, "suite", change.Suite, change.Result, change.DryRun)
	}
	fmt.Fprintln(env.Stdout, archiveSentence(change))
	return nil
}

func suiteIDForArchive(client *api.Client, org, want string) (string, error) {
	if tokenID.MatchString(want) {
		return want, nil
	}
	suites, err := client.SuitesIn(org, "all")
	if err != nil {
		return "", err
	}
	for _, s := range suites {
		if strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(want)) {
			return s.ID, nil
		}
	}
	return "", contract.Errorf(contract.SuiteNotFound, "no suite %q in %s; see: sel suites --org %s", want, org, org)
}

func archiveSentence(change *api.SuiteChange) string {
	switch {
	case change.Result == "unchanged":
		return fmt.Sprintf("Suite %q is already archived.", change.Name)
	case change.DryRun:
		return fmt.Sprintf("Dry run: would archive suite %q.", change.Name)
	}
	return fmt.Sprintf("Archived suite %q.", change.Name)
}
