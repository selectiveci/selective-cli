package doctor

import (
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
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
}

func Run(opts Options) *Report {
	return &Report{Status: "pass"}
}

func (r *Report) Err() error {
	return nil
}
