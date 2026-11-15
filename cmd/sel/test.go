package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/selectiveci/selective-cli/internal/localrun"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/spf13/cobra"
)

func transportCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__transport",
		Hidden:             true,
		DisableFlagParsing: true,
		Annotations:        map[string]string{"mcp": "skip"},
		Run: func(_ *cobra.Command, args []string) {
			if len(args) == 1 && args[0] == "--stdio" {
				signal.Ignore(syscall.SIGINT, syscall.SIGTERM)
			}
			os.Exit(localrun.RunSeat(args, os.Stdin, os.Stdout, os.Stderr))
		},
	}
}

type testFlags struct {
	against, framework, suite, org string
	timeout                        time.Duration
}

var languages = map[localrun.Framework]string{localrun.RSpec: "ruby", localrun.Minitest: "ruby", localrun.Pytest: "python"}

func testCmd() *cobra.Command {
	var f testFlags
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run the tests a change affects, on this machine",
		Long: `Run the tests that the change in this checkout affects, using the suite's
recorded test map to pick them.

The suite is the one in the organization whose repository is this checkout's
origin remote; --suite names one. The change is every tracked file that differs from the merge base of HEAD and
--against, committed or not; untracked files are left out. --against defaults
to the remote's default branch (origin/HEAD, else origin/main, else
origin/master).

Changed file paths and test ids are sent to Selective; file contents are not.
A map that cannot speak for the change runs every test, and the selection
event says so. Arguments after -- go to the test framework's client. Its
output goes to stderr; stdout carries NDJSON events only, ending with a
summary.`,
		Example: `  sel test
  sel test --against origin/main --json
  sel test --suite 3f9c -- -k slow`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && cmd.ArgsLenAtDash() != 0 {
				return contract.Errorf(contract.UsageError, "unexpected argument %q; pass framework arguments after --", args[0])
			}
			return nil
		},
		Annotations: map[string]string{
			"exit_codes":  "0,1,3,4,5,6,8,130",
			"events":      "build_start,selection,test_fail,progress,heartbeat,summary",
			"sends_paths": "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := runTest(cmd, args, f)
			if err == nil && code != 0 {
				os.Exit(code)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&f.against, "against", "", "Compare with this ref (default: the remote's default branch)")
	cmd.Flags().StringVar(&f.framework, "framework", "", "Test framework: rspec, minitest or pytest (default: detected)")
	cmd.Flags().StringVar(&f.suite, "suite", "", "Suite ID (default: the suite for this checkout's origin)")
	cmd.Flags().StringVar(&f.org, "org", "", "Organization slug (default: SELECTIVE_ORG, or your only one)")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 30*time.Minute, "Stop the run after this long")
	return cmd
}

type testPlan struct {
	root      string
	framework localrun.Framework
	selector  localrun.Selector
}

type plannedTest struct {
	plan testPlan
	err  error
}

func runTest(cmd *cobra.Command, args []string, f testFlags) (int, error) {
	if f.timeout <= 0 {
		return 0, contract.Errorf(contract.UsageError, "--timeout must be positive")
	}
	started := time.Now()
	signalled, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	signalledAt := make(chan time.Time, 1)
	context.AfterFunc(signalled, func() { signalledAt <- time.Now() })
	stdout := cmd.OutOrStdout()
	events := stdout
	if !format.JSONMode(cmd.Flags(), stdout) {
		events = prettyEvents{format.NewWriter(stdout, "pretty")}
	}
	interrupted := func(sum localrun.Summary) (int, error) {
		fmt.Fprintln(cmd.ErrOrStderr(), "Interrupted")
		if !sum.Reported {
			sum.MS = (<-signalledAt).Sub(started).Milliseconds()
			if err := localrun.WriteInterruptedSummary(events, sum); err != nil {
				return 0, contract.Errorf(contract.InternalError, "could not write the event stream: %v", err)
			}
		}
		return contract.ExitInterrupted, nil
	}
	planned := make(chan plannedTest, 1)
	go func() {
		plan, err := planTest(args, f)
		planned <- plannedTest{plan, err}
	}()
	var plan testPlan
	select {
	case p := <-planned:
		if p.err != nil {
			return 0, p.err
		}
		plan = p.plan
	case <-signalled.Done():
		return interrupted(localrun.Summary{})
	}
	self, err := os.Executable()
	if err != nil {
		return 0, contract.Errorf(contract.InternalError, "%v", err)
	}
	ctx, cancel := context.WithTimeout(signalled, f.timeout)
	defer cancel()
	launch := localrun.Launch{Root: plan.root, Framework: plan.framework, Args: args, Sel: self, Stderr: cmd.ErrOrStderr()}
	sum, err := launch.Run(ctx, events, localrun.Options{Selector: plan.selector, Timeout: f.timeout})
	switch {
	case errors.Is(err, localrun.ErrInterrupted) && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return 0, contract.Errorf(contract.Timeout, "the test run did not finish within %s", f.timeout)
	case errors.Is(err, localrun.ErrInterrupted):
		return interrupted(sum)
	case err != nil:
		return 0, err
	case sum.Failed > 0:
		return contract.ExitFailures, nil
	}
	return 0, nil
}

func planTest(args []string, f testFlags) (testPlan, error) {
	root, err := os.Getwd()
	if err != nil {
		return testPlan{}, contract.Errorf(contract.InternalError, "%v", err)
	}
	framework, err := localrun.DetectFramework(root, f.framework)
	if err != nil {
		return testPlan{}, err
	}
	changed, err := localrun.ChangedFiles(root, f.against)
	if err != nil {
		return testPlan{}, err
	}
	env, err := setup.Connect("", false)
	if err != nil {
		return testPlan{}, err
	}
	org, err := env.ResolveOrg(f.org)
	if err != nil {
		return testPlan{}, err
	}
	suite, err := resolveSuite(env.Client, org, f.suite)
	if err != nil {
		return testPlan{}, err
	}
	return testPlan{root: root, framework: framework, selector: selectTests(env.Client, org, suite, framework, changed)}, nil
}

func resolveSuite(client *api.Client, org, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	repo, err := gitctx.Repo()
	if err != nil {
		return "", contract.Errorf(contract.UsageError, "pass --suite ID (could not read this checkout's origin: %v)", err)
	}
	suites, err := client.Suites(org)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, s := range suites {
		if s.Repo != nil && strings.EqualFold(*s.Repo, repo) {
			ids = append(ids, s.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", contract.Errorf(contract.SuiteNotFound, "no suite in %s for %s; see: sel suites --org %s", org, repo, org)
	case 1:
		return ids[0], nil
	}
	return "", contract.Errorf(contract.UsageError, "several suites in %s use %s (%s); pass --suite ID", org, repo, strings.Join(ids, ", "))
}

func selectTests(client *api.Client, org, suite string, framework localrun.Framework, changed []string) localrun.Selector {
	return func(_ context.Context, manifest localrun.Manifest) (localrun.Selection, error) {
		var tree struct {
			Prefix string `json:"prefix"`
		}
		_ = json.Unmarshal(manifest.Tree, &tree)
		req := api.SelectionRequest{SuiteID: suite, Language: languages[framework], Framework: string(framework), Prefix: tree.Prefix, Changed: changed}
		for _, c := range manifest.Cases {
			req.TestCases = append(req.TestCases, api.SelectionCase{ID: c.ID, FilePath: c.FilePath})
		}
		picked, err := client.SelectTests(org, req)
		if err != nil {
			return localrun.Selection{}, err
		}
		selection := localrun.Selection{Outcome: picked.Outcome, Cause: picked.Cause, Selected: picked.Selected, ChangedFiles: picked.Counts.ChangedFiles}
		if picked.Map != nil {
			selection.Source = "map"
		}
		return selection, nil
	}
}

type prettyEvents struct{ w *format.Writer }

func (p prettyEvents) Write(line []byte) (int, error) {
	return len(line), p.w.Write(bytes.TrimSpace(line))
}
