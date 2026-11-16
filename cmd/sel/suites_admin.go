package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/approvals"
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
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6", "mutates": "true", "idempotent": "true"},
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
	suites, err := client.SuitesIn(org, "all")
	if err != nil {
		return "", err
	}
	for _, s := range suites {
		if s.ID == want {
			return s.ID, nil
		}
	}
	var wanted, other []string
	for _, s := range suites {
		if !strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(want)) {
			continue
		}
		if (s.ArchivedAt == nil) == archive {
			wanted = append(wanted, s.ID)
		} else {
			other = append(other, s.ID)
		}
	}
	if len(wanted) == 0 && len(other) > 1 {
		wanted = other
	}
	switch {
	case len(wanted) == 1:
		return wanted[0], nil
	case len(wanted) > 1:
		return "", contract.Errorf(contract.UsageError, "%d suites in %s are named %q (%s); pass one of the ids", len(wanted), org, want, strings.Join(wanted, ", "))
	case len(other) == 1:
		return other[0], nil
	case tokenID.MatchString(want):
		return want, nil
	}
	return "", contract.Errorf(contract.SuiteNotFound, "no suite %q in %s; see: sel suites --org %s", want, org, org)
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

func suitesDeleteCmd() *cobra.Command {
	var f setupFlags
	var yes, dryRun bool
	gate := &approvals.Gate{}
	cmd := &cobra.Command{
		Use:   "delete SUITE",
		Short: "Delete an archived suite for good",
		Long: `Delete an archived suite (by name or ID) and everything stored under it.
It cannot be undone. Needs a token with the write scope, as an owner or admin
of the organization.

A suite must be archived first (sel suites archive): an active suite exits 5
(suite_not_archived), and so does a suite that still has runs
(suite_has_runs).

--yes is required, with or without a terminal. --dry-run reports what would
happen, changes nothing and needs no --yes.

An agent token needs a person's approval first: without --wait the command
exits 7 with the approval to decide. --wait[=10m] waits for the decision, then
deletes the suite and ends with a done line; --timeout bounds the whole run
(exit 8).`,
		Example:     "  sel suites delete old-minitest --yes\n  sel suites delete old-minitest --yes --wait=10m --json\n  sel suites delete old-minitest --dry-run",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{"exit_codes": "0,3,4,5,6,7,8", "mutates": "true", "destructive": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			gate.Started = time.Now()
			if !yes && !dryRun {
				return contract.Errorf(contract.UsageError, "deleting a suite needs --yes (or --dry-run to preview)")
			}
			env, _, err := f.env(false)
			if err != nil {
				return err
			}
			env.Stdout, env.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			gate.Client, gate.Out, gate.Err, gate.JSON = env.Client, env.Stdout, env.Stderr, env.JSON
			return suitesDelete(cmd.Context(), env, gate, f.org, args[0], dryRun)
		},
	}
	f.bind(cmd, true)
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the delete (required, with or without a terminal)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be deleted without deleting it")
	approvals.BindWait(cmd, gate)
	return cmd
}

func suitesDelete(ctx context.Context, env *setup.Env, gate *approvals.Gate, orgFlag, want string, dryRun bool) error {
	var org, id string
	send := func(ctx context.Context) (*approvals.Outcome, error) {
		scoped := *env
		scoped.Client = env.Client.WithContext(ctx)
		if id == "" {
			var err error
			if org, err = scoped.ResolveOrg(orgFlag); err != nil {
				return nil, err
			}
			if id, err = suiteIDForArchive(scoped.Client, org, want, false); err != nil {
				return nil, err
			}
		}
		change, err := scoped.Client.DeleteSuite(org, id, dryRun)
		if err != nil {
			return nil, err
		}
		return &approvals.Outcome{Key: "suite", Resource: change.Suite, Result: change.Result, DryRun: change.DryRun}, nil
	}
	return gate.Do(ctx, send, func(o *approvals.Outcome) string { return deletedSentence(want, o) })
}

func deletedSentence(name string, outcome *approvals.Outcome) string {
	if outcome.DryRun {
		return fmt.Sprintf("Dry run: would delete suite %q.", name)
	}
	return fmt.Sprintf("Deleted suite %q.", name)
}
