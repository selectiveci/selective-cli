// Package api is a minimal client for Selective's /api/v1 endpoints.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrUnauthorized means the token was rejected (revoked, mistyped, expired).
var ErrUnauthorized = errors.New("unauthorized")

type Client struct {
	Host  string
	Token string
	HTTP  *http.Client
}

func New(host, token string) *Client {
	return &Client{Host: host, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type Me struct {
	UserID   string   `json:"user_id"`
	Email    string   `json:"email"`
	OrgSlugs []string `json:"org_slugs"`
}

func (c *Client) Me() (*Me, error) {
	var me Me
	if err := c.do("GET", "/api/v1/me", nil, http.StatusOK, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

// RevokeCurrent revokes the token the client is using.
func (c *Client) RevokeCurrent() error {
	return c.do("DELETE", "/api/v1/tokens/current", nil, http.StatusNoContent, nil)
}

func (c *Client) do(method, path string, body any, want int, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.Host+path, reader)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", c.Host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: server returned %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("unexpected response from %s: %w", path, err)
		}
	}
	return nil
}
