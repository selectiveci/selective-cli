package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/credentials"
	"github.com/selectiveci/selective-cli/internal/gitctx"
	"github.com/selectiveci/selective-cli/internal/setup"
	"github.com/selectiveci/selective-cli/internal/update"
)

var (
	httpTimeout = 10 * time.Second
	settleGrace = 250 * time.Millisecond
)

const (
	loginFix = "sel auth login --setup"
	suiteFix = "sel suites create --name <name> --framework <type>"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

type Check struct {
	Name    string         `json:"name"`
	Status  Status         `json:"status"`
	Code    contract.Code  `json:"code,omitempty"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
	Fix     string         `json:"fix,omitempty"`
	MS      *int64         `json:"ms,omitempty"`
}

type Report struct {
	Status string  `json:"status"`
	Checks []Check `json:"checks"`
}

type Options struct {
	Hostname string
	Org      string
	Timeout  time.Duration
	Repo     func() (string, error)
	Root     func() (string, error)
	OS       string
}

type state struct {
	ctx     context.Context
	opts    Options
	cfg     *config.Config
	host    string
	secret  string
	cred    *credentials.Credential
	credErr error
	client  *api.Client
	me      *api.Me
	meErr   error
	orgSlug string

	suiteTypes []string

	advertised advertised
}

type step struct {
	name  string
	after string
	run   func(*state) Check
}

var steps = []step{
	{"config", "", (*state).checkConfig},
	{"server", "config", (*state).checkServer},
	{"auth", "server", (*state).checkAuth},
	{"org", "auth", (*state).checkOrg},
	{"suite", "org", (*state).checkSuite},
}

func Run(opts Options) *Report {
	if opts.Repo == nil {
		opts.Repo = gitctx.Repo
	}
	if opts.Root == nil {
		opts.Root = os.Getwd
	}
	if opts.OS == "" {
		opts.OS = runtime.GOOS
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	s := &state{ctx: ctx, opts: opts}
	blockedBy := map[string]string{}
	expired := false
	report := &Report{Status: "pass"}
	for _, st := range steps {
		var c Check
		switch by := blockedBy[st.after]; {
		case by != "":
			c = Check{Name: st.name, Status: Skip, Message: "skipped because " + by + " failed"}
			blockedBy[st.name] = by
		case expired:
			c = Check{Name: st.name, Status: Skip, Message: "skipped because the run's timeout expired"}
		case ctx.Err() != nil:
			c = timedOut(st.name, opts.Timeout, Check{})
			blockedBy[st.name], expired = st.name, true
		default:
			c = runWithin(ctx, s, st)
			if c.Status == Fail {
				blockedBy[st.name] = st.name
				expired = c.Code == contract.Timeout
			}
		}
		report.Checks = append(report.Checks, c)
	}
	report.Checks = append(report.Checks, Check{Name: "socket", Status: Skip, Message: "added in a later release"})
	if expired {
		report.Checks = append(report.Checks, Check{Name: "runner", Status: Skip, Message: "skipped because the run's timeout expired"})
	} else {
		report.Checks = append(report.Checks, s.checkRunner())
	}
	switch by := blockedBy["server"]; {
	case expired:
		report.Checks = append(report.Checks, Check{Name: "cli_version", Status: Skip, Message: "skipped because the run's timeout expired"})
	case by != "":
		report.Checks = append(report.Checks, Check{Name: "cli_version", Status: Skip, Message: "skipped because " + by + " failed"})
	default:
		report.Checks = append(report.Checks, s.checkCLIVersion())
	}
	report.Status = summarize(report.Checks)
	return report
}

func runWithin(ctx context.Context, s *state, st step) Check {
	done := make(chan Check, 1)
	go func() {
		c := st.run(s)
		done <- c.redact(s.secret)
	}()
	select {
	case c := <-done:
		if c.Status == Fail && ctx.Err() != nil {
			return timedOut(st.name, s.opts.Timeout, c)
		}
		return c
	case <-ctx.Done():
		select {
		case c := <-done:
			return timedOut(st.name, s.opts.Timeout, c)
		case <-time.After(settleGrace):
			return timedOut(st.name, s.opts.Timeout, Check{})
		}
	}
}

func timedOut(name string, limit time.Duration, attempted Check) Check {
	c := fail(name, contract.Timeout, fmt.Sprintf("did not finish within %s", limit), "")
	if n := attempted.attempts(); n > 1 {
		c.Detail = map[string]any{"attempts": n}
	}
	return c
}

func (c Check) attempts() int {
	switch n := c.Detail["attempts"].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func attemptsOf(err error) int {
	var fielded contract.Fielded
	if !errors.As(err, &fielded) {
		return 0
	}
	n, _ := fielded.ErrorFields()["attempts"].(int)
	return n
}

func (c Check) redact(secret string) Check {
	if secret == "" {
		return c
	}
	c.Message = strings.ReplaceAll(c.Message, secret, "[redacted]")
	c.Fix = strings.ReplaceAll(c.Fix, secret, "[redacted]")
	if c.Detail != nil {
		c.Detail = redactDetail(c.Detail, secret)
	}
	return c
}

func redactDetail(detail map[string]any, secret string) map[string]any {
	raw, err := json.Marshal(detail)
	if err != nil {
		return map[string]any{}
	}
	quoted, _ := json.Marshal(secret)
	clean := strings.ReplaceAll(string(raw), string(quoted[1:len(quoted)-1]), "[redacted]")
	var out map[string]any
	if json.Unmarshal([]byte(clean), &out) != nil {
		return map[string]any{}
	}
	return out
}

func summarize(checks []Check) string {
	status := "pass"
	for _, c := range checks {
		switch c.Status {
		case Fail:
			return "fail"
		case Warn:
			status = "warn"
		}
	}
	return status
}

func (r *Report) Err() error {
	for _, c := range r.Checks {
		if c.Status == Fail {
			fields := map[string]any{"doctor": r}
			if n := c.attempts(); n > 1 {
				fields["attempts"] = n
			}
			return &contract.Error{Code: c.Code, Message: c.Message, Fields: fields}
		}
	}
	return nil
}

func fail(name string, code contract.Code, message, fix string) Check {
	return Check{Name: name, Status: Fail, Code: code, Message: message, Fix: fix}
}

func failFrom(name string, err error, fix string) Check {
	return fail(name, codeOf(err), err.Error(), fix)
}

func codeOf(err error) contract.Code {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusConflict && (!contract.Registered(apiErr.Code) || apiErr.Code == contract.InternalError) {
			return contract.UsageError
		}
		if contract.Registered(apiErr.Code) {
			return apiErr.Code
		}
		return api.CodeForStatus(apiErr.Status)
	}
	var coded contract.Coded
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return contract.InternalError
}

func unreachable(err error) (contract.Code, bool) {
	var apiErr *api.Error
	switch {
	case err == nil:
		return "", false
	case !errors.As(err, &apiErr):
		return contract.InternalError, true
	case apiErr.Status == 0:
		return contract.NetworkError, true
	case apiErr.Status >= 500:
		return contract.ServerError, true
	}
	return "", false
}

func (s *state) checkConfig() Check {
	cfg, err := config.Load()
	if err != nil {
		return fail("config", contract.UsageError, fmt.Sprintf("could not load config: %v", err), "fix or delete "+config.Path())
	}
	host, err := cfg.ResolveHost(s.opts.Hostname)
	if err != nil {
		return failFrom("config", err, "fix --hostname, SELECTIVE_HOST, or current_host in "+config.Path())
	}
	s.cfg, s.host = cfg, host
	return Check{Name: "config", Status: OK, Message: "using " + host, Detail: map[string]any{"host": host, "path": config.Path()}}
}

func (s *state) checkServer() Check {
	s.cred, s.credErr = credentials.Lookup(s.cfg, s.host)
	token := ""
	if s.credErr == nil {
		token = s.cred.Token
	}
	s.secret = token
	s.client = api.New(s.host, token).WithContext(s.ctx)
	s.client.HTTP.Transport = recordingTransport{base: s.client.HTTP.Transport, seen: &s.advertised}
	s.client.HTTP.Timeout = httpTimeout

	began := time.Now()
	s.me, s.meErr = s.client.Me()
	ms := time.Since(began).Milliseconds()

	if code, down := unreachable(s.meErr); down {
		c := fail("server", code, s.meErr.Error(), "")
		if n := attemptsOf(s.meErr); n > 1 {
			c.Detail = map[string]any{"attempts": n}
		}
		c.MS = &ms
		return c
	}
	return Check{Name: "server", Status: OK, Message: fmt.Sprintf("%s answered in %dms", s.host, ms), MS: &ms}
}

func (s *state) loginCommand() string {
	if s.host == config.DefaultHost {
		return loginFix
	}
	return loginFix + " --hostname " + s.host
}

func (s *state) checkAuth() Check {
	if s.credErr != nil {
		return fail("auth", contract.Unauthorized, "not logged in to "+s.host, s.loginCommand())
	}
	if s.meErr != nil {
		if errors.Is(s.meErr, api.ErrUnauthorized) {
			fix := s.loginCommand()
			if s.cred.Source == credentials.SourceEnv {
				fix = "replace or unset SELECTIVE_TOKEN, then run doctor again"
			}
			return fail("auth", contract.Unauthorized, fmt.Sprintf("the server rejected the token from %s: %v", s.cred.Source, s.meErr), fix)
		}
		return failFrom("auth", s.meErr, "")
	}
	return Check{
		Name:    "auth",
		Status:  OK,
		Message: fmt.Sprintf("logged in as %s (token from %s)", s.me.Email, s.cred.Source),
		Detail:  map[string]any{"email": s.me.Email, "source": string(s.cred.Source)},
	}
}

func (s *state) checkOrg() Check {
	slug, err := (&setup.Env{Client: s.client}).ResolveOrg(s.opts.Org)
	if err != nil {
		fix := ""
		if codeOf(err) == contract.UsageError {
			fix = "pass --org SLUG or set SELECTIVE_ORG"
		}
		return failFrom("org", err, fix)
	}
	orgs, err := s.client.Organizations()
	if err != nil {
		return failFrom("org", err, "")
	}
	slugs := make([]string, len(orgs))
	for i, o := range orgs {
		slugs[i] = o.Slug
	}
	if !slices.Contains(slugs, slug) {
		return fail("org", contract.OrganizationNotFound, fmt.Sprintf("no organization %q among yours: %s", slug, strings.Join(slugs, ", ")), "sel orgs")
	}
	s.orgSlug = slug
	return Check{Name: "org", Status: OK, Message: "using organization " + slug, Detail: map[string]any{"slug": slug}}
}

func (s *state) checkSuite() Check {
	repo, err := s.opts.Repo()
	if err != nil {
		return Check{Name: "suite", Status: Warn, Message: "no git origin remote here, so no suite to match; run from a checkout"}
	}
	all, err := s.client.Suites(s.orgSlug)
	if err != nil {
		return failFrom("suite", err, "")
	}
	var matches []map[string]any
	var names, types []string
	for _, su := range all {
		if su.Repo != nil && strings.EqualFold(*su.Repo, repo) {
			if su.Type != nil && *su.Type != "" {
				types = append(types, *su.Type)
			}
			matches = append(matches, map[string]any{"name": su.Name, "type": su.Type, "last_run_at": su.LastRunAt})
			names = append(names, su.Name)
		}
	}
	if len(matches) == 0 {
		return fail("suite", contract.SuiteNotFound, fmt.Sprintf("no suite for %s in %s", repo, s.orgSlug), suiteFix)
	}
	s.suiteTypes = types
	return Check{
		Name:    "suite",
		Status:  OK,
		Message: fmt.Sprintf("%s: %s", repo, strings.Join(names, ", ")),
		Detail:  map[string]any{"repo": repo, "suites": matches},
	}
}

func (s *state) checkCLIVersion() Check {
	info := update.Status(update.Version, "", s.advertised.state())
	if info.LatestVersion == "" && info.MinVersion == "" {
		return Check{Name: "cli_version", Status: Skip, Message: "no version information: a dev build, or the server advertised none"}
	}
	detail := map[string]any{}
	if info.LatestVersion != "" {
		detail["latest_version"] = info.LatestVersion
	}
	if info.MinVersion != "" {
		detail["min_version"] = info.MinVersion
	}
	belowMinimum := info.BelowMinimum != nil && *info.BelowMinimum
	if info.BelowMinimum != nil {
		detail["below_minimum"] = belowMinimum
	}
	switch {
	case belowMinimum:
		return Check{Name: "cli_version", Status: Warn, Message: fmt.Sprintf("sel %s is below the server's minimum %s", info.Version, info.MinVersion), Detail: detail, Fix: info.UpgradeCommand}
	case info.UpdateAvailable != nil && *info.UpdateAvailable:
		return Check{Name: "cli_version", Status: Warn, Message: fmt.Sprintf("sel %s can be upgraded to %s", info.Version, info.LatestVersion), Detail: detail, Fix: info.UpgradeCommand}
	case info.LatestVersion == "":
		return Check{Name: "cli_version", Status: OK, Message: fmt.Sprintf("sel %s meets the server's minimum %s", info.Version, info.MinVersion), Detail: detail}
	}
	return Check{Name: "cli_version", Status: OK, Message: fmt.Sprintf("sel %s is the latest release", info.Version), Detail: detail}
}
