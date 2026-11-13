// Package setup implements `sel orgs`, `sel repos` and
// `sel suites`: what an agent needs to set a repository up.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/mattn/go-isatty"
	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/credentials"
	"github.com/selectiveci/selective-cli/internal/gitctx"
)

// Env is what every command shares.
type Env struct {
	Client *api.Client
	Stdout io.Writer
	Stderr io.Writer
	// JSON output: forced by --json, and the default when stdout isn't a terminal.
	JSON bool
	// StdoutIsTerminal guards the API key; swapped in tests.
	StdoutIsTerminal bool
	// RunGh runs gh with stdin; swapped in tests.
	RunGh func(stdin string, args ...string) error
}

// Connect builds an Env for the logged-in host.
func Connect(hostname string, forceJSON bool) (*Env, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("could not load config: %w", err)
	}
	host, err := cfg.ResolveHost(hostname)
	if err != nil {
		return nil, err
	}
	cred, err := credentials.Lookup(cfg, host)
	if err != nil {
		return nil, fmt.Errorf("not logged in to %s; run: sel auth login --setup", host)
	}
	tty := isatty.IsTerminal(os.Stdout.Fd())
	return &Env{
		Client:           api.New(host, cred.Token),
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		JSON:             forceJSON || !tty,
		StdoutIsTerminal: tty,
		RunGh:            runGh,
	}, nil
}

func (e *Env) json(v any) error {
	enc := json.NewEncoder(e.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (e *Env) table(header string, rows [][]string) {
	w := tabwriter.NewWriter(e.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}

// Orgs lists the organizations the login belongs to.
func (e *Env) Orgs() error {
	orgs, err := e.Client.Organizations()
	if err != nil {
		return err
	}
	if e.JSON {
		return e.json(map[string]any{"organizations": orgs})
	}
	rows := [][]string{}
	for _, o := range orgs {
		rows = append(rows, []string{o.Slug, o.Name, o.Role, dash(o.GithubAccount)})
	}
	e.table("SLUG\tNAME\tROLE\tGITHUB", rows)
	return nil
}

// ResolveOrg picks the organization: the flag, SELECTIVE_ORG, or the only one.
func (e *Env) ResolveOrg(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("SELECTIVE_ORG"); env != "" {
		return env, nil
	}
	orgs, err := e.Client.Organizations()
	if err != nil {
		return "", err
	}
	switch len(orgs) {
	case 0:
		return "", errors.New("this login belongs to no organization yet; finish signing up in the browser")
	case 1:
		return orgs[0].Slug, nil
	}
	slugs := make([]string, len(orgs))
	for i, o := range orgs {
		slugs[i] = o.Slug
	}
	return "", fmt.Errorf("you belong to several organizations; pass --org (one of: %s)", strings.Join(slugs, ", "))
}

// Repos lists the repositories a suite can be created for.
func (e *Env) Repos(org string) error {
	repos, err := e.Client.Repos(org)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.json(repos)
	}
	if len(repos.Repos) == 0 {
		fmt.Fprintf(e.Stdout, "Selective can't see any repositories yet. Add them to the GitHub App at:\n  %s\n", repos.InstallURL)
		if repos.IntegrationsURL != "" {
			fmt.Fprintf(e.Stdout, "or connect GitLab at:\n  %s\n", repos.IntegrationsURL)
		}
		return nil
	}
	rows := [][]string{}
	for _, r := range repos.Repos {
		vis := "public"
		if r.Private {
			vis = "private"
		}
		host := r.Host
		if host == "" {
			host = "github.com"
		}
		rows = append(rows, []string{r.FullName, host, vis})
	}
	e.table("REPOSITORY\tHOST\tVISIBILITY", rows)
	fmt.Fprintf(e.Stdout, "\nAdd GitHub repositories at %s\n", repos.InstallURL)
	if repos.IntegrationsURL != "" {
		fmt.Fprintf(e.Stdout, "Connect GitLab at %s\n", repos.IntegrationsURL)
	}
	return nil
}

// Suites lists the organization's suites.
func (e *Env) Suites(org string) error {
	suites, err := e.Client.Suites(org)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.json(map[string]any{"suites": suites})
	}
	rows := [][]string{}
	for _, s := range suites {
		last := "never"
		if s.LastRunAt != nil {
			last = s.LastRunAt.Local().Format("2006-01-02 15:04")
		}
		rows = append(rows, []string{s.Name, deref(s.Repo), deref(s.Type), dash(s.SelectionMode), last, s.ID})
	}
	e.table("NAME\tREPOSITORY\tFRAMEWORK\tSELECTION\tLAST RUN\tID", rows)
	return nil
}

// CreateSuite finds or creates a suite. repo defaults to this checkout's origin.
func (e *Env) CreateSuite(org, name, repo, framework string) error {
	repo, provider, err := repoOrOrigin(repo)
	if err != nil {
		return err
	}
	suite, result, err := e.Client.CreateSuite(org, name, repo, provider, framework)
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.InstallURL != "" {
		if provider == "gitlab" {
			return fmt.Errorf("%s\n  Ask the user to connect GitLab in Selective's Integrations settings with access to %s, then try again", apiErr.Message, repo)
		}
		return fmt.Errorf("%s\n  Ask the user to open %s and give the app access to %s, then try again", apiErr.Message, apiErr.InstallURL, repo)
	}
	if err != nil {
		return err
	}
	if e.JSON {
		return e.json(map[string]any{"suite": suite, "result": result})
	}
	verb := map[string]string{
		"created":         "Created",
		"existing":        "Found existing",
		"adopted_default": "Set up the organization's default suite as",
	}[result]
	fmt.Fprintf(e.Stdout, "✓ %s suite %q for %s (id %s)\n", verb, suite.Name, deref(suite.Repo), suite.ID)
	return nil
}

// SelectionModes are what --selection accepts.
var SelectionModes = []string{"off", "shadow", "live"}

// Configure changes a suite's settings.
func (e *Env) Configure(org, suite string, settings api.SuiteSettings) error {
	if settings.SelectionMode == "" && settings.SelectionOnDefaultBranch == nil {
		return errors.New("say what to change, e.g. --selection shadow")
	}
	if m := settings.SelectionMode; m != "" && !slices.Contains(SelectionModes, m) {
		return fmt.Errorf("--selection must be one of: %s", strings.Join(SelectionModes, ", "))
	}
	s, err := e.findSuite(org, suite)
	if err != nil {
		return err
	}
	updated, previous, err := e.Client.ConfigureSuite(org, s.ID, settings)
	if err != nil {
		return err
	}
	if e.JSON {
		return e.json(map[string]any{"suite": updated, "previous": previous})
	}
	if settings.SelectionMode != "" {
		if previous.SelectionMode == updated.SelectionMode {
			fmt.Fprintf(e.Stdout, "✓ Selection for %q is already %s\n", updated.Name, updated.SelectionMode)
		} else {
			fmt.Fprintf(e.Stdout, "✓ Selection for %q: %s → %s\n", updated.Name, dash(previous.SelectionMode), updated.SelectionMode)
		}
	}
	if settings.SelectionOnDefaultBranch != nil {
		was, now := defaultBranch(previous.SelectionOnDefaultBranch), defaultBranch(updated.SelectionOnDefaultBranch)
		if was == now {
			fmt.Fprintf(e.Stdout, "✓ Default-branch runs for %q already: %s\n", updated.Name, now)
		} else {
			fmt.Fprintf(e.Stdout, "✓ Default-branch runs for %q: %s → %s\n", updated.Name, was, now)
		}
	}
	return nil
}

// DefaultBranchModes are what --default-branch accepts.
var DefaultBranchModes = []string{"full", "select"}

func defaultBranch(selects *bool) string {
	switch {
	case selects == nil:
		return "-"
	case *selects:
		return "select"
	default:
		return "full"
	}
}

// KeyOptions say where `suites key` sends the key.
type KeyOptions struct {
	GithubSecret string // set this GitHub Actions secret, via gh
	Repo         string // the secret's repository; default the suite's
	Stdout       bool   // write the key to stdout, which must be a pipe
}

// Key hands a suite's API key on without displaying it: into a GitHub
// Actions secret, or, with --stdout, down a pipe. Nothing is written
// unless the caller names where the key goes.
func (e *Env) Key(org, suite string, opts KeyOptions) error {
	switch {
	case opts.GithubSecret == "" && !opts.Stdout:
		return errors.New("say where the key goes: --github-secret NAME, " +
			"or --stdout piped straight into a secret store (sel suites key NAME --stdout | <secret command>)")
	case opts.GithubSecret != "" && opts.Stdout:
		return errors.New("use --github-secret or --stdout, not both")
	case opts.Stdout && e.StdoutIsTerminal:
		return errors.New("refusing to print an API key to a terminal; pipe --stdout into a secret store")
	}
	s, err := e.findSuite(org, suite)
	if err != nil {
		return err
	}
	key, err := e.Client.SuiteAPIKey(org, s.ID)
	if err != nil {
		return err
	}
	if opts.Stdout {
		_, err := io.WriteString(e.Stdout, key)
		return err
	}

	repo := opts.Repo
	if repo == "" {
		repo = deref(s.Repo)
	}
	if repo == "" {
		return errors.New("the suite has no repository yet; pass --repo owner/name")
	}
	if err := e.RunGh(key, "secret", "set", opts.GithubSecret, "--repo", repo); err != nil {
		return fmt.Errorf("could not set the GitHub secret: %w", err)
	}
	if e.JSON {
		return e.json(map[string]any{"secret": opts.GithubSecret, "repo": repo, "suite": s.Name})
	}
	fmt.Fprintf(e.Stdout, "✓ Set GitHub Actions secret %s on %s to suite %q's API key\n", opts.GithubSecret, repo, s.Name)
	return nil
}

// findSuite matches by ID, then by name (ignoring case).
func (e *Env) findSuite(org, want string) (*api.Suite, error) {
	suites, err := e.Client.Suites(org)
	if err != nil {
		return nil, err
	}
	for i := range suites {
		if suites[i].ID == want {
			return &suites[i], nil
		}
	}
	for i := range suites {
		if strings.EqualFold(strings.TrimSpace(suites[i].Name), strings.TrimSpace(want)) {
			return &suites[i], nil
		}
	}
	return nil, fmt.Errorf("no suite %q in %s; see: sel suites --org %s", want, org, org)
}

// repoOrOrigin returns repo, or this checkout's origin path and provider.
func repoOrOrigin(repo string) (string, string, error) {
	if repo != "" {
		return repo, "", nil
	}
	path, provider, err := gitctx.Origin()
	if err != nil {
		return "", "", fmt.Errorf("pass --repo owner/name (couldn't read this checkout's origin: %w)", err)
	}
	return path, provider, nil
}

func runGh(stdin string, args ...string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("the GitHub CLI (gh) isn't installed; pipe the key into your CI's secret store instead")
	}
	cmd := exec.Command("gh", args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
