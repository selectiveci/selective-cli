package api

import (
	"net/http"
	"net/url"
	"strconv"
)

type FailureSuite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Failure struct {
	ID          string       `json:"id"`
	RunID       string       `json:"run_id"`
	Suite       FailureSuite `json:"suite"`
	TestID      *string      `json:"test_id"`
	File        string       `json:"file"`
	Line        int          `json:"line"`
	Error       *string      `json:"error"`
	Node        string       `json:"node"`
	Outcome     string       `json:"outcome"`
	Attempts    int          `json:"attempts"`
	RerunStatus *string      `json:"rerun_status"`
	FlakeID     *string      `json:"flake_id"`
	FlakeStatus *string      `json:"flake_status"`
	Provisional bool         `json:"provisional"`
}

type FailuresPage struct {
	BuildID                  string    `json:"build_id"`
	Status                   string    `json:"status"`
	Archived                 bool      `json:"archived"`
	ArchivedFailedCount      int       `json:"archived_failed_count"`
	ArchivedFlakyFailedCount int       `json:"archived_flaky_failed_count"`
	Failures                 []Failure `json:"failures"`
	NextCursor               *string   `json:"next_cursor"`
	Notices                  []string  `json:"notices,omitempty"`
}

type FailuresQuery struct {
	Org     string
	Outcome string
	Limit   int
	Cursor  string
}

func (c *Client) Failures(build string, q FailuresQuery) (*FailuresPage, error) {
	params := url.Values{}
	if q.Org != "" {
		params.Set("org", q.Org)
	}
	if q.Outcome != "" {
		params.Set("outcome", q.Outcome)
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Cursor != "" {
		params.Set("cursor", q.Cursor)
	}
	path := "/api/v1/builds/" + url.PathEscape(build) + "/failures"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var out FailuresPage
	if err := c.do("GET", path, nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
