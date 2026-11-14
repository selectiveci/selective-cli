package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/localrun"
)

func TestFixtureDrivesALaunchedRunnerThroughTheFakeServer(t *testing.T) {
	cases := []struct {
		name    string
		reply   string
		ran     int
		outcome string
	}{
		{"one test selected", selected, 1, "select"},
		{"nothing selected", nothing, 0, "select"},
		{"everything selected", everything, 3, "full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAPI(t, tc.reply)
			ran := runnerEnv(t)
			client := api.New(fake.srv.URL, "tok")
			selector := func(_ context.Context, manifest localrun.Manifest) (localrun.Selection, error) {
				picked, err := client.SelectTests("acme", api.SelectionRequest{SuiteID: "suite-1", Framework: "pytest", Changed: []string{"src/calc.py"}})
				if err != nil {
					return localrun.Selection{}, err
				}
				return localrun.Selection{Outcome: picked.Outcome, Cause: picked.Cause, Selected: picked.Selected}, nil
			}
			var events bytes.Buffer
			var stderr bytes.Buffer
			launch := localrun.Launch{Root: pytestProject(t), Framework: localrun.Pytest, Sel: os.Args[0], Stderr: &stderr}

			if _, err := launch.Run(context.Background(), &events, localrun.Options{Selector: selector}); err != nil {
				t.Fatalf("launch: %v\nstderr: %s", err, stderr.String())
			}

			docs := stdoutEvents(t, events.String())
			if got := eventNames(docs); got[len(got)-1] != "summary" {
				t.Errorf("events = %v, want them to end with a summary", got)
			}
			if sel := firstNamed(docs, "selection"); sel["outcome"] != tc.outcome {
				t.Errorf("selection = %v, want outcome %q", sel, tc.outcome)
			}
			if got := ranIDs(t, ran); len(got) != tc.ran {
				t.Errorf("ran %v, want %d tests", got, tc.ran)
			}
			if body := fake.firstSelection(t); body["suite_id"] != "suite-1" || fmt.Sprint(body["changed"]) != "[src/calc.py]" {
				t.Errorf("selection request = %v", body)
			}
		})
	}
}
