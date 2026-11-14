package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/contract"
)

var laterChecks = []string{"socket", "runner", "cli_version"}

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
	Timeout  time.Duration
}

type state struct {
	opts   Options
	cfg    *config.Config
	host   string
	secret string
}

type step struct {
	name  string
	after string
	run   func(*state) Check
}

var steps = []step{
	{"config", "", (*state).checkConfig},
}

func Run(opts Options) *Report {
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	s := &state{opts: opts}
	blockedBy := map[string]string{}
	report := &Report{Status: "pass"}
	for _, st := range steps {
		var c Check
		switch by := blockedBy[st.after]; {
		case by != "":
			c = Check{Name: st.name, Status: Skip, Message: "skipped because " + by + " failed"}
			blockedBy[st.name] = by
		case ctx.Err() != nil:
			c = Check{Name: st.name, Status: Skip, Message: "skipped because the run's timeout expired"}
		default:
			c = runWithin(ctx, s, st)
			if c.Status == Fail {
				blockedBy[st.name] = st.name
			}
		}
		report.Checks = append(report.Checks, c)
	}
	for _, name := range laterChecks {
		report.Checks = append(report.Checks, Check{Name: name, Status: Skip, Message: "added in a later release"})
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
		return c
	case <-ctx.Done():
		return fail(st.name, contract.Timeout, fmt.Sprintf("did not finish within %s", s.opts.Timeout), "")
	}
}

func (c Check) redact(secret string) Check {
	if secret == "" {
		return c
	}
	c.Message = strings.ReplaceAll(c.Message, secret, "[redacted]")
	c.Fix = strings.ReplaceAll(c.Fix, secret, "[redacted]")
	return c
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
			return &contract.Error{Code: c.Code, Message: c.Message, Fields: map[string]any{"doctor": r}}
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
