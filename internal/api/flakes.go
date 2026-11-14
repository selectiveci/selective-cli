package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

type FlakesQuery struct {
	Suite  string
	Status string
	Sort   string
	Age    string
	Search string
	Limit  int
	Cursor string
}

type FlakesPage struct {
	Flakes     []json.RawMessage `json:"flakes"`
	NextCursor *string           `json:"next_cursor"`
	Notices    []string          `json:"notices"`
}

type FlakePrompt struct {
	Text string
	raw  json.RawMessage
}

func (p *FlakePrompt) UnmarshalJSON(data []byte) error {
	var fields struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	p.Text, p.raw = fields.Text, append(json.RawMessage(nil), data...)
	return nil
}

func (p FlakePrompt) MarshalJSON() ([]byte, error) { return p.raw, nil }

func (c *Client) Flakes(org string, q FlakesQuery) (*FlakesPage, error) {
	params := url.Values{}
	for name, value := range map[string]string{
		"suite": q.Suite, "status": q.Status, "sort": q.Sort, "age": q.Age, "search": q.Search, "cursor": q.Cursor,
	} {
		if value != "" {
			params.Set(name, value)
		}
	}
	if q.Limit > 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	var out FlakesPage
	if err := c.do("GET", withQuery("/api/v1/orgs/"+url.PathEscape(org)+"/flakes", params), nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Flake(id string, history *int) (json.RawMessage, error) {
	params := url.Values{}
	if history != nil {
		params.Set("history", strconv.Itoa(*history))
	}
	var out struct {
		Flake json.RawMessage `json:"flake"`
	}
	if err := c.do("GET", withQuery("/api/v1/flakes/"+url.PathEscape(id), params), nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return out.Flake, nil
}

func (c *Client) FlakePrompt(id string) (*FlakePrompt, error) {
	var out struct {
		Prompt FlakePrompt `json:"prompt"`
	}
	if err := c.do("GET", "/api/v1/flakes/"+url.PathEscape(id)+"/prompt", nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Prompt, nil
}

func withQuery(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.Encode()
}
