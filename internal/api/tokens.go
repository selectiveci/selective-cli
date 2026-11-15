package api

import "net/http"

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

func (c *Client) CurrentToken() (*Token, error) {
	var out struct {
		Token Token `json:"token"`
	}
	if err := c.do("GET", "/api/v1/tokens/current", nil, http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out.Token, nil
}
