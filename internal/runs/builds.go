package runs

import (
	"encoding/json"
	"fmt"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

type BuildsOptions struct {
	Org    string
	Status string
	Flaky  *bool
	Branch string
	Limit  int
	Cursor string
	All    bool
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
	return format.Document(env.Stdout, page)
}

func fetchBuilds(client *api.Client, org string, opts BuildsOptions) (*api.BuildsPage, error) {
	query := api.BuildsQuery{Status: opts.Status, Flaky: opts.Flaky, Branch: opts.Branch, Limit: opts.Limit}
	if opts.All && query.Limit == 0 {
		query.Limit = pageSize
	}
	var last *api.BuildsPage
	builds, next, stoppedEarly, err := walk(opts.Cursor, opts.All, func(cursor string) ([]json.RawMessage, *string, error) {
		query.Cursor = cursor
		page, err := client.Builds(org, query)
		if err != nil {
			return nil, nil, err
		}
		last = page
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
