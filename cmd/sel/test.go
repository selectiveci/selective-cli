package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"syscall"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
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
}

var languages = map[localrun.Framework]string{localrun.RSpec: "ruby", localrun.Minitest: "ruby", localrun.Pytest: "python"}

func testCmd() *cobra.Command {
	var f testFlags
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run the tests a change affects, on this machine",
		Long: `Run the tests that the change in this checkout affects, using the suite's
recorded test map to pick them.

The change is every tracked file that differs from the merge base of HEAD and
--against, committed or not; untracked files are left out. --against defaults
to the remote's default branch (origin/HEAD, else origin/main, else
origin/master).

Changed file paths and test ids are sent to Selective; file contents are not.
A map that cannot speak for the change runs every test, and the selection
event says so. Arguments after -- go to the test framework's client. Its
output goes to stderr; stdout carries NDJSON events only, ending with a
summary.`,
		Example: `  sel test --suite 3f9c
  sel test --suite 3f9c --against origin/main --json
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
	cmd.Flags().StringVar(&f.suite, "suite", "", "Suite ID")
	cmd.Flags().StringVar(&f.org, "org", "", "Organization slug (default: SELECTIVE_ORG, or your only one)")
	_ = cmd.MarkFlagRequired("suite")
	return cmd
}

func runTest(cmd *cobra.Command, args []string, f testFlags) (int, error) {
	root, err := os.Getwd()
	if err != nil {
		return 0, contract.Errorf(contract.InternalError, "%v", err)
	}
	framework, err := localrun.DetectFramework(root, f.framework)
	if err != nil {
		return 0, err
	}
	changed, err := localrun.ChangedFiles(root, f.against)
	if err != nil {
		return 0, err
	}
	env, err := setup.Connect("", false)
	if err != nil {
		return 0, err
	}
	org, err := env.ResolveOrg(f.org)
	if err != nil {
		return 0, err
	}
	self, err := os.Executable()
	if err != nil {
		return 0, contract.Errorf(contract.InternalError, "%v", err)
	}
	stdout := cmd.OutOrStdout()
	events := stdout
	if !format.JSONMode(cmd.Flags(), stdout) {
		events = prettyEvents{format.NewWriter(stdout, "pretty")}
	}
	launch := localrun.Launch{Root: root, Framework: framework, Args: args, Sel: self, Stderr: cmd.ErrOrStderr()}
	sum, err := launch.Run(cmd.Context(), events, localrun.Options{Selector: selectTests(env.Client, org, f.suite, framework, changed)})
	switch {
	case err != nil:
		return 0, err
	case sum.Failed > 0:
		return contract.ExitFailures, nil
	}
	return 0, nil
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
