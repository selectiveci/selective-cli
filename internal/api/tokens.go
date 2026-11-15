package api

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

const (
	tokensPageSize         = 200
	maxTokens              = 20000
	maxTokenRequests       = 2000
	PaginationStoppedEarly = "pagination_stopped_early"
)

type Token struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Prefix       string   `json:"prefix"`
	Kind         string   `json:"kind"`
	Scopes       []string `json:"scopes"`
	Organization *string  `json:"organization"`
	Repos        []string `json:"repos"`
	ExpiresAt    *string  `json:"expires_at"`
	Source       string   `json:"source"`
	InsertedAt   string   `json:"inserted_at"`
}

type TokensPage struct {
	Tokens     []Token  `json:"tokens"`
	NextCursor *string  `json:"next_cursor"`
	Notices    []string `json:"notices,omitempty"`
}

func (c *Client) Tokens() (*TokensPage, error) {
	merged := &TokensPage{Tokens: []Token{}}
	params := url.Values{"limit": {strconv.Itoa(tokensPageSize)}}
	for requests := 0; requests < maxTokenRequests; requests++ {
		var page TokensPage
		if err := c.do("GET", withQuery("/api/v1/tokens", params), nil, http.StatusOK, &page); err != nil {
			return nil, err
		}
		merged.Tokens = append(merged.Tokens, page.Tokens...)
		merged.Notices = appendMissing(merged.Notices, page.Notices...)
		merged.NextCursor = page.NextCursor
		if page.NextCursor == nil || len(merged.Tokens) >= maxTokens {
			break
		}
		params.Set("cursor", *page.NextCursor)
	}
	if merged.NextCursor != nil {
		merged.Notices = appendMissing(merged.Notices, PaginationStoppedEarly)
	}
	return merged, nil
}

type RevokedToken struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

type Revocation struct {
	Token  RevokedToken `json:"token"`
	Result string       `json:"result"`
	DryRun bool         `json:"dry_run"`
}

func (c *Client) RevokeToken(id string, dryRun bool) (*Revocation, error) {
	params := url.Values{}
	if dryRun {
		params.Set("dry_run", "true")
	}
	var out Revocation
	if err := c.do("DELETE", withQuery("/api/v1/tokens/"+url.PathEscape(id), params), nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func appendMissing(notices []string, add ...string) []string {
	for _, n := range add {
		if !slices.Contains(notices, n) {
			notices = append(notices, n)
		}
	}
	return notices
}

func (c *Client) CurrentToken() (*Token, error) {
	var out struct {
		Token Token `json:"token"`
	}
	if err := c.do("GET", "/api/v1/tokens/current", nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Token, nil
}
