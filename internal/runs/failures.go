package runs

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/format"
	"github.com/selectiveci/selective-cli/internal/setup"
)

const (
	pageSize         = 200
	maxPages         = 100
	errorWidth       = 100
	provisionalMarks = "*"
)

type FailuresOptions struct {
	Build    string
	Hostname string
	Org      string
	Suite    string
	Outcome  string
	Limit    int
	Cursor   string
}

func Failures(env *setup.Env, opts FailuresOptions) error {
	page, err := fetchFailures(env.Client, opts)
	if err != nil {
		return err
	}
	if env.JSON {
		return format.Document(env.Stdout, page)
	}
	renderFailures(env, opts, page)
	return nil
}

func fetchFailures(client *api.Client, opts FailuresOptions) (*api.FailuresPage, error) {
	query := api.FailuresQuery{Org: opts.Org, Outcome: opts.Outcome, Limit: opts.Limit, Cursor: opts.Cursor}
	single := opts.Limit > 0
	if !single {
		query.Limit = pageSize
	}
	var merged *api.FailuresPage
	for pages := 0; pages < maxPages; pages++ {
		page, err := client.Failures(opts.Build, query)
		if err != nil {
			return nil, err
		}
		page.Failures = filterSuite(page.Failures, opts.Suite)
		if merged != nil {
			page.Failures = append(merged.Failures, page.Failures...)
		}
		merged = page
		if single || page.NextCursor == nil {
			break
		}
		query.Cursor = *page.NextCursor
	}
	if merged.Failures == nil {
		merged.Failures = []api.Failure{}
	}
	return merged, nil
}

func filterSuite(failures []api.Failure, suite string) []api.Failure {
	if suite == "" {
		return failures
	}
	kept := []api.Failure{}
	for _, f := range failures {
		if f.Suite.Name == suite || f.Suite.ID == suite {
			kept = append(kept, f)
		}
	}
	return kept
}

func renderFailures(env *setup.Env, opts FailuresOptions, page *api.FailuresPage) {
	if len(page.Failures) == 0 {
		fmt.Fprintf(env.Stderr, "No %s results in build %s.\n", describeOutcome(opts.Outcome), page.BuildID)
		if page.Archived {
			fmt.Fprintf(env.Stderr, "The build is archived: %d failed and %d flaky results were counted, but their details were deleted. Restore the run to see them.\n",
				page.ArchivedFailedCount, page.ArchivedFlakyFailedCount)
		}
	} else {
		renderTable(env, page)
	}
	if page.NextCursor != nil {
		fmt.Fprintf(env.Stderr, "More results: %s\n", continueCommand(opts, page))
	}
}

func renderTable(env *setup.Env, page *api.FailuresPage) {
	w := tabwriter.NewWriter(env.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "OUTCOME\tSUITE\tLOCATION\tATTEMPTS\tERROR")
	hasProvisional := false
	for _, f := range page.Failures {
		outcome := f.Outcome
		if f.Provisional {
			outcome += provisionalMarks
			hasProvisional = true
		}
		fmt.Fprintf(w, "%s\t%s\t%s:%d\t%d\t%s\n", outcome, f.Suite.Name, f.File, f.Line, f.Attempts, firstLine(f.Error))
	}
	_ = w.Flush()
	if hasProvisional {
		fmt.Fprintf(env.Stderr, "%s the build is still running or a rerun is pending; this may yet pass.\n", provisionalMarks)
	}
}

func continueCommand(opts FailuresOptions, page *api.FailuresPage) string {
	parts := []string{"sel failures", shellQuote(page.BuildID)}
	for _, flag := range []struct{ name, value string }{
		{"--hostname", opts.Hostname},
		{"--org", opts.Org},
		{"--outcome", opts.Outcome},
		{"--suite", opts.Suite},
	} {
		if flag.value != "" {
			parts = append(parts, flag.name, shellQuote(flag.value))
		}
	}
	if opts.Limit > 0 {
		parts = append(parts, "--limit", strconv.Itoa(opts.Limit))
	}
	return strings.Join(append(parts, "--cursor", shellQuote(*page.NextCursor)), " ")
}

var hostOS = runtime.GOOS

func shellQuote(value string) string { return quoteFor(hostOS, value) }

func quoteFor(goos, value string) string {
	escaped := `'\''`
	if goos == "windows" {
		escaped = "''"
	}
	return "'" + strings.ReplaceAll(value, "'", escaped) + "'"
}

func describeOutcome(outcome string) string {
	if outcome == "" {
		return "failed"
	}
	return outcome
}

func firstLine(s *string) string {
	if s == nil {
		return "-"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(*s), "\n")
	runes := []rune(line)
	if len(runes) > errorWidth {
		return string(runes[:errorWidth]) + "…"
	}
	return line
}
