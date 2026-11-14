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

func withQuery(path string, params url.Values) string {
	if len(params) == 0 {
		return path
	}
	return path + "?" + params.Encode()
}
