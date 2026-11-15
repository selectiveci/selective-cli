package api

import (
	"net/http"
	"net/url"
)

type SelectionCase struct {
	ID       string `json:"id"`
	FilePath string `json:"file_path"`
}

type SelectionRequest struct {
	SuiteID   string          `json:"suite_id"`
	Language  string          `json:"language"`
	Framework string          `json:"framework"`
	Prefix    string          `json:"prefix,omitempty"`
	Changed   []string        `json:"changed"`
	TestCases []SelectionCase `json:"test_cases"`
}

type Selection struct {
	Outcome  string   `json:"outcome"`
	Cause    string   `json:"cause"`
	Selected []string `json:"selected"`
	Map      *struct {
		ID string `json:"id"`
	} `json:"map"`
	Counts struct {
		Total        int `json:"total"`
		Selected     int `json:"selected"`
		Skipped      int `json:"skipped"`
		ChangedFiles int `json:"changed_files"`
	} `json:"counts"`
}

func (c *Client) SelectTests(org string, req SelectionRequest) (*Selection, error) {
	if req.Changed == nil {
		req.Changed = []string{}
	}
	if req.TestCases == nil {
		req.TestCases = []SelectionCase{}
	}
	var out struct {
		Selection Selection `json:"selection"`
	}
	err := c.do("POST", "/api/v1/orgs/"+url.PathEscape(org)+"/selections", req, http.StatusOK, &out)
	return &out.Selection, err
}
