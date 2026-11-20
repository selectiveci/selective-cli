package tail

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
)

type budgetRun struct {
	ID         string `json:"id"`
	Applied    *bool  `json:"applied"`
	MaxRetries *int   `json:"max_retries"`
}

func setRetryBudget(ctx context.Context, client *api.Client, buildID string, opts Options) error {
	written, err := client.WithContext(ctx).SetRetryBudget(buildID, opts.IdempotencyKey, opts.Retries, opts.DryRun)
	if err != nil {
		return err
	}
	runs := budgetRuns(written)
	switch {
	case opts.Format == "ndjson":
		if err := format.Document(outWriter, written); err != nil {
			return err
		}
	case opts.DryRun:
		fmt.Fprintln(outWriter, budgetHeadline(buildID, opts, runs))
	default:
		fmt.Fprintln(errWriter, budgetHeadline(buildID, opts, runs))
	}
	if !opts.DryRun {
		for _, note := range budgetNotes(runs, opts.Retries) {
			fmt.Fprintln(errWriter, note)
		}
	}
	return nil
}

func budgetRuns(written json.RawMessage) []budgetRun {
	var body struct {
		RetryBudget struct {
			Runs []budgetRun `json:"runs"`
		} `json:"retry_budget"`
	}
	if json.Unmarshal(written, &body) != nil {
		return nil
	}
	return body.RetryBudget.Runs
}

func taken(run budgetRun) bool { return run.Applied == nil || *run.Applied }

func budgetHeadline(buildID string, opts Options, runs []budgetRun) string {
	if opts.DryRun {
		return fmt.Sprintf("Dry run: would set the retry budget of build %s to %d on %d live run(s); nothing changed.", buildID, opts.Retries, len(runs))
	}
	var effective []int
	for _, run := range runs {
		if taken(run) && run.MaxRetries != nil && !slices.Contains(effective, *run.MaxRetries) {
			effective = append(effective, *run.MaxRetries)
		}
	}
	slices.Sort(effective)
	switch {
	case len(runs) > 0 && !slices.ContainsFunc(runs, taken):
		return fmt.Sprintf("No run of build %s took the retry budget (not live).", buildID)
	case len(effective) == 0, len(effective) == 1 && effective[0] == opts.Retries:
		return fmt.Sprintf("Set the retry budget of build %s to %d.", buildID, opts.Retries)
	}
	values := make([]string, len(effective))
	for i, n := range effective {
		values[i] = strconv.Itoa(n)
	}
	return fmt.Sprintf("Set the retry budget of build %s to %s (you asked for %d).", buildID, strings.Join(values, " and "), opts.Retries)
}

func budgetNotes(runs []budgetRun, requested int) []string {
	var notes []string
	for _, run := range runs {
		switch {
		case !taken(run):
			notes = append(notes, fmt.Sprintf("Run %s did not take the retry budget (not live).", run.ID))
		case run.MaxRetries != nil && *run.MaxRetries != requested:
			notes = append(notes, fmt.Sprintf("Run %s is retrying up to %d times, not the %d you asked for.", run.ID, *run.MaxRetries, requested))
		}
	}
	return notes
}
