package runs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type BuildsOptions struct {
	Hostname string
	Org      string
	Status   string
	Flaky    *bool
	Branch   string
	Limit    int
	Cursor   string
	All      bool
}

type BuildOptions struct {
	ID  string
	Org string
}

func Builds(env *setup.Env, opts BuildsOptions) error {
	org, err := env.ResolveOrg(opts.Org)
	if err != nil {
		return err
	}
	page, err := fetchBuilds(env.Client, org, opts)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, page)
	}
	return renderBuilds(env, org, opts, page)
}

func fetchBuilds(client *api.Client, org string, opts BuildsOptions) (*api.BuildsPage, error) {
	query := api.BuildsQuery{Status: opts.Status, Flaky: opts.Flaky, Branch: opts.Branch, Limit: opts.Limit}
	if opts.All && query.Limit == 0 {
		query.Limit = pageSize
	}
	var last *api.BuildsPage
	var notices []string
	builds, next, stoppedEarly, err := walk(opts.Cursor, opts.All, func(cursor string) ([]json.RawMessage, *string, error) {
		query.Cursor = cursor
		page, err := client.Builds(org, query)
		if err != nil {
			return nil, nil, err
		}
		last = page
		for _, notice := range page.Notices {
			if !slices.Contains(notices, notice) {
				notices = append(notices, notice)
			}
		}
		return page.Builds, page.NextCursor, nil
	})
	if err != nil {
		return nil, err
	}
	last.Builds = builds
	if last.Builds == nil {
		last.Builds = []json.RawMessage{}
	}
	last.NextCursor = next
	last.Notices = notices
	if stoppedEarly {
		last.Notices = append(last.Notices, paginationStoppedEarly)
	}
	return last, nil
}

func Build(env *setup.Env, opts BuildOptions) error {
	build, err := env.Client.Build(opts.ID, opts.Org)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Resource(env.Stdout, "build", build)
	}
	return renderBuild(env, build)
}

var buildColumns = []column{
	{"ID", "id", nil},
	{"STATUS", "status", nil},
	{"BRANCH", "branch", nil},
	{"TESTS", "total", nil},
	{"FAILED", "failed", nil},
	{"FLAKY", "flaky", nil},
	{"STARTED", "started_at", nil},
	{"DURATION", "duration_ms", millis},
	{"TITLE", "title", nil},
}

var buildFacts = []column{
	{"ID", "id", nil},
	{"Title", "title", nil},
	{"Status", "status", nil},
	{"Branch", "branch", nil},
	{"SHA", "sha", nil},
	{"Repo", "repo", nil},
	{"Actor", "actor", nil},
	{"Suites", "suite_count", nil},
	{"Tests", "total", nil},
	{"Failed", "failed", nil},
	{"Flaky", "flaky", nil},
	{"Started", "started_at", nil},
	{"Completed", "completed_at", nil},
	{"Duration", "duration_ms", millis},
	{"Archived", "archived", nil},
}

var runColumns = []column{
	{"RUN", "id", nil},
	{"SUITE", "name", nil},
	{"STATUS", "status", nil},
	{"TESTS", "total", nil},
	{"FAILED", "failed", nil},
	{"FLAKY", "flaky", nil},
}

func renderBuilds(env *setup.Env, org string, opts BuildsOptions, page *api.BuildsPage) error {
	if len(page.Builds) == 0 {
		fmt.Fprintln(env.Stderr, "No builds match.")
	} else {
		rows := [][]string{headers(buildColumns)}
		for _, raw := range page.Builds {
			build, err := decodeFields(raw)
			if err != nil {
				return err
			}
			rows = append(rows, build.cells(buildColumns))
		}
		table(env.Stdout, rows)
	}
	for _, notice := range page.Notices {
		fmt.Fprintf(env.Stderr, "Notice: %s\n", noticeSentence(notice, len(page.Builds), "builds"))
	}
	if page.NextCursor != nil {
		fmt.Fprintf(env.Stderr, "More results: %s\n", buildsContinuation(org, opts, *page.NextCursor))
	}
	return nil
}

func buildsContinuation(org string, opts BuildsOptions, cursor string) string {
	parts := []string{"sel builds"}
	for _, flag := range []struct{ name, value string }{
		{"--hostname", opts.Hostname},
		{"--org", org},
		{"--status", opts.Status},
	} {
		if flag.value != "" {
			parts = append(parts, flag.name, shellQuote(flag.value))
		}
	}
	if opts.Flaky != nil {
		parts = append(parts, "--flaky="+strconv.FormatBool(*opts.Flaky))
	}
	if opts.Branch != "" {
		parts = append(parts, "--branch", shellQuote(opts.Branch))
	}
	if opts.Limit > 0 {
		parts = append(parts, "--limit", strconv.Itoa(opts.Limit))
	}
	if opts.All {
		parts = append(parts, "--all")
	}
	return strings.Join(append(parts, "--cursor", shellQuote(cursor)), " ")
}

func renderBuild(env *setup.Env, raw json.RawMessage) error {
	build, err := decodeFields(raw)
	if err != nil {
		return err
	}
	facts := make([][]string, len(buildFacts))
	for i, c := range buildFacts {
		facts[i] = []string{c.header, build.cell(c)}
	}
	table(env.Stdout, facts)
	var runs []json.RawMessage
	_ = json.Unmarshal(build["runs"], &runs)
	if len(runs) == 0 {
		return nil
	}
	rows := [][]string{headers(runColumns)}
	for _, raw := range runs {
		run, err := decodeFields(raw)
		if err != nil {
			return err
		}
		cells := run.cells(runColumns)
		cells[1] = run.object("suite").cell(runColumns[1])
		rows = append(rows, cells)
	}
	fmt.Fprintln(env.Stdout)
	table(env.Stdout, rows)
	return nil
}
