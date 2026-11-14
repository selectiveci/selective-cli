package runs

import (
	"encoding/json"
	"fmt"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type TrendsOptions struct {
	Org     string
	Range   string
	Suite   string
	Section string
	Limit   int
}

func Trends(env *setup.Env, opts TrendsOptions) error {
	org, err := env.ResolveOrg(opts.Org)
	if err != nil {
		return err
	}
	document, err := env.Client.Trends(org, api.TrendsQuery{Range: opts.Range, Suite: opts.Suite, Section: opts.Section, Limit: opts.Limit})
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, document)
	}
	return renderTrends(env, document)
}

type trendsSection struct {
	name    string
	title   string
	columns []column
}

var trendsSections = []trendsSection{
	{"outcomes", "OUTCOMES", []column{
		{"BUCKET", "bucket_start", nil}, {"PASSED", "passed_count", nil}, {"FAILED", "failed_count", nil},
		{"INCOMPLETE", "incomplete_count", nil}, {"CANCELLED", "cancelled_count", nil},
	}},
	{"failures", "FAILURES", []column{
		{"BUCKET", "bucket_start", nil}, {"FAILURES", "failures_count", nil},
	}},
	{"duration", "DURATION", []column{
		{"BUCKET", "bucket_start", nil}, {"AVG", "avg_build_s", seconds}, {"P95", "p95_build_s", seconds}, {"BUILDS", "build_count", nil},
	}},
	{"top_failing", "TOP FAILING", []column{
		{"TEST", "test_name", nil}, {"SUITE", "suite_name", nil}, {"FAILS", "fail_count", nil}, {"RUNS", "run_count", nil},
	}},
	{"most_flaky", "MOST FLAKY", []column{
		{"TEST", "test_name", nil}, {"SUITE", "suite_name", nil}, {"STATUS", "status", nil},
		{"OCCURRENCES", "occurrence_count", nil}, {"BRANCHES", "branch_count", nil},
	}},
	{"slowest", "SLOWEST", []column{
		{"TEST", "test_name", nil}, {"SUITE", "suite_name", nil}, {"AVG", "avg_s", seconds}, {"P95", "p95_s", seconds},
		{"MAX", "max_s", seconds}, {"RUNS", "run_count", nil}, {"TREND", "trend", nil},
	}},
}

type summaryRow struct {
	label    string
	current  column
	previous *column
}

var summaryRows = []summaryRow{
	{"Tests", column{key: "tests_count"}, nil},
	{"Pass rate", column{key: "pass_rate", format: percent}, &column{key: "pass_rate", format: percent}},
	{"Failures", column{key: "failures_count"}, &column{key: "failures_count"}},
	{"Flaky", column{key: "flaky_count"}, nil},
	{"Median build", column{key: "median_build_s", format: seconds}, &column{key: "median_build_s", format: seconds}},
}

func renderTrends(env *setup.Env, document map[string]json.RawMessage) error {
	top := fields(document)
	if _, ok := top["window"]; ok {
		window := top.object("window")
		fmt.Fprintf(env.Stderr, "Window: %s, %s to %s, in %s buckets.\n",
			top.cell(column{key: "range"}), window.cell(column{key: "from"}), window.cell(column{key: "to"}), window.cell(column{key: "bucket"}))
	}
	printed := false
	if raw, ok := document["summary"]; ok {
		summary, err := decodeFields(raw)
		if err != nil {
			return err
		}
		fmt.Fprintln(env.Stdout, "SUMMARY")
		renderSummary(env, summary)
		printed = true
	}
	for _, section := range trendsSections {
		raw, ok := document[section.name]
		if !ok {
			continue
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return contract.Errorf(contract.ServerError, "unexpected response: %v", err)
		}
		if printed {
			fmt.Fprintln(env.Stdout)
		}
		fmt.Fprintln(env.Stdout, section.title)
		printed = true
		if len(rows) == 0 {
			fmt.Fprintf(env.Stderr, "No rows in %s.\n", section.name)
			continue
		}
		cells := [][]string{headers(section.columns)}
		for _, row := range rows {
			f, err := decodeFields(row)
			if err != nil {
				return err
			}
			cells = append(cells, f.cells(section.columns))
		}
		table(env.Stdout, cells)
	}
	return nil
}

func renderSummary(env *setup.Env, summary fields) {
	previous := summary.object("previous")
	rows := [][]string{{"METRIC", "THIS WINDOW", "PREVIOUS"}}
	for _, r := range summaryRows {
		before := notTrackedCell
		if r.previous != nil {
			before = previous.cell(*r.previous)
		}
		rows = append(rows, []string{r.label, summary.cell(r.current), before})
	}
	table(env.Stdout, rows)
}
