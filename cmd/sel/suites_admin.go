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
	return suitesArchivedCmd(true, "archive", "Archive a suite", `Archive a suite (by name or ID): it leaves the default suite list and its
flakes are hidden, while its runs and builds stay readable. Needs a token with
the write scope, as an owner or admin of the organization. Reversible with
sel suites restore.

Archiving an archived suite changes nothing and exits 0 with result
"unchanged". --dry-run reports what would happen and changes nothing.`)
}

func suitesRestoreCmd() *cobra.Command {
	return suitesArchivedCmd(false, "restore", "Restore an archived suite", `Restore an archived suite (by name or ID) to the active list. Needs a token
with the write scope, as an owner or admin of the organization.

Restoring an active suite changes nothing and exits 0 with result
"unchanged". --dry-run reports what would happen and changes nothing.`)
}

func suitesArchivedCmd(archive bool, verb, short, long string) *cobra.Command {
	var f setupFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:         verb + " SUITE",
		Short:       short,
		Long:        long,
		Example:     fmt.Sprintf("  sel suites %s web-rspec\n  sel suites %s web-rspec --dry-run --json", verb, verb),
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			env, org, err := f.env(true)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			return suitesSetArchived(env, org, args[0], archive, dryRun)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would change without changing it")
	return cmd
}

func suitesSetArchived(env *setup.Env, org, want string, archive, dryRun bool) error {
	id, err := suiteIDForArchive(env.Client, org, want, archive)
	if err != nil {
		return err
	}
	change, err := env.Client.SetSuiteArchived(org, id, archive, dryRun)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Write(env.Stdout, "suite", change.Suite, change.Result, change.DryRun)
	}
	fmt.Fprintln(env.Stdout, archivedSentence(change, archive))
	return nil
}

func suiteIDForArchive(client *api.Client, org, want string, archive bool) (string, error) {
	if tokenID.MatchString(want) {
		return want, nil
	}
	suites, err := client.SuitesIn(org, "all")
	if err != nil {
		return "", err
	}
	var found *api.ListedSuite
	for i, s := range suites {
		if !strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(want)) {
			continue
		}
		if (s.ArchivedAt == nil) == archive {
			return s.ID, nil
		}
		if found == nil {
			found = &suites[i]
		}
	}
	if found == nil {
		return "", contract.Errorf(contract.SuiteNotFound, "no suite %q in %s; see: sel suites --org %s", want, org, org)
	}
	return found.ID, nil
}

func archivedSentence(change *api.SuiteChange, archive bool) string {
	state, verb := "active", "restore"
	if archive {
		state, verb = "archived", "archive"
	}
	switch {
	case change.Result == "unchanged":
		return fmt.Sprintf("Suite %q is already %s.", change.Name, state)
	case change.DryRun:
		return fmt.Sprintf("Dry run: would %s suite %q.", verb, change.Name)
	case archive:
		return fmt.Sprintf("Archived suite %q.", change.Name)
	}
	return fmt.Sprintf("Restored suite %q.", change.Name)
}
