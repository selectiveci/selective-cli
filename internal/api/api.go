// Package api is a minimal client for Selective's /api/v1 endpoints.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

type Client struct {
	Host    string
	Token   string
	HTTP    *http.Client
	ctx     context.Context
	noRetry bool
}

func (c *Client) WithContext(ctx context.Context) *Client {
	bound := *c
	bound.ctx = ctx
	return &bound
}

func (c *Client) WithoutRetry() *Client {
	bound := *c
	bound.noRetry = true
	return &bound
}

func New(host, token string) *Client {
	return &Client{Host: host, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type Me struct {
	UserID   string   `json:"user_id"`
	Email    string   `json:"email"`
	OrgSlugs []string `json:"org_slugs"`
	Scopes   []string `json:"scopes"`
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

func (c *Client) requestContext() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *Client) do(method, path string, body any, want int, out any) error {
	return c.doWith(nil, method, path, body, want, out)
}

func (c *Client) doWith(header http.Header, method, path string, body any, want int, out any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return err
		}
	}
	retryable := !c.noRetry && (method == http.MethodGet || header.Get("Idempotency-Key") != "")
	ctx := c.requestContext()
	clk := currentClock()
	for attempt := 1; ; attempt++ {
		err := c.attempt(header, method, path, data, want, out)
		if err == nil || !retryable || attempt == maxAttempts || ctx.Err() != nil || !transient(err) {
			return err
		}
		if sleepErr := clk.Sleep(ctx, backoff(attempt)); sleepErr != nil {
			return networkError(c.Host, sleepErr)
		}
	}
}

func (c *Client) attempt(header http.Header, method, path string, data []byte, want int, out any) error {
	var reader io.Reader
	if data != nil {
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(c.requestContext(), method, c.Host+path, reader)
	if err != nil {
		return err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return networkError(c.Host, err)
	}
	defer resp.Body.Close()

	acceptable := resp.StatusCode == want ||
		resp.StatusCode == http.StatusAccepted ||
		(want == http.StatusOK && resp.StatusCode == http.StatusCreated)
	if !acceptable {
		return decodeError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			code := contract.ServerError
			var netErr net.Error
			if errors.As(err, &netErr) {
				code = contract.NetworkError
			}
			return &Error{Status: resp.StatusCode, Code: code, Message: fmt.Sprintf("unexpected response from %s: %v", path, err), cause: err}
		}
	}
	return nil
}

type Organization struct {
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	GithubAccount string `json:"github_account"`
}

func (c *Client) Organizations() ([]Organization, error) {
	var out struct {
		Organizations []Organization `json:"organizations"`
	}
	err := c.do("GET", "/api/v1/orgs", nil, http.StatusOK, &out)
	return out.Organizations, err
}

type Repos struct {
	GithubAccount string `json:"github_account"`
	InstallURL    string `json:"install_url"`
	Repos         []struct {
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	} `json:"repos"`
}

func (c *Client) Repos(org string) (*Repos, error) {
	var out Repos
	err := c.do("GET", "/api/v1/orgs/"+url.PathEscape(org)+"/repos", nil, http.StatusOK, &out)
	return &out, err
}

type Suite struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Type      *string    `json:"type"`
	Repo      *string    `json:"repo"`
	LastRunAt *time.Time `json:"last_run_at"`
	// SelectionMode is off, shadow or live; empty from servers that predate it.
	SelectionMode string `json:"selection_mode,omitempty"`
	// SelectionOnDefaultBranch is nil from servers that predate it.
	SelectionOnDefaultBranch *bool `json:"selection_on_default_branch,omitempty"`
}

func (c *Client) Suites(org string) ([]Suite, error) {
	var out struct {
		Suites []Suite `json:"suites"`
	}
	err := c.do("GET", "/api/v1/orgs/"+url.PathEscape(org)+"/suites", nil, http.StatusOK, &out)
	return out.Suites, err
}

type CreatedSuite struct {
	Suite   Suite  `json:"suite"`
	Result  string `json:"result"`
	Changed bool   `json:"changed"`
}

func (c *Client) CreateSuite(org, name, repo, framework string) (*CreatedSuite, error) {
	var out CreatedSuite
	body := map[string]string{"name": name, "repo": repo}
	if framework != "" {
		body["type"] = framework
	}
	err := c.do("POST", "/api/v1/orgs/"+url.PathEscape(org)+"/suites", body, http.StatusOK, &out)
	return &out, err
}

func (c *Client) SuiteAPIKey(org, suiteID string) (string, error) {
	var out struct {
		APIKey string `json:"api_key"`
	}
	err := c.do("GET", "/api/v1/orgs/"+url.PathEscape(org)+"/suites/"+url.PathEscape(suiteID)+"/api_key", nil, http.StatusOK, &out)
	return out.APIKey, err
}

// SuiteSettings are the settings `ConfigureSuite` can change. Empty fields stay as they are.
type SuiteSettings struct {
	SelectionMode            string `json:"selection_mode,omitempty"`
	SelectionOnDefaultBranch *bool  `json:"selection_on_default_branch,omitempty"`
}

// ConfigureSuite changes a suite's settings and returns the suite with the
// settings it had before.
func (c *Client) ConfigureSuite(org, suiteID string, settings SuiteSettings) (*Suite, SuiteSettings, error) {
	var out struct {
		Suite    Suite         `json:"suite"`
		Previous SuiteSettings `json:"previous"`
	}
	err := c.do("PATCH", "/api/v1/orgs/"+url.PathEscape(org)+"/suites/"+url.PathEscape(suiteID), settings, http.StatusOK, &out)
	return &out.Suite, out.Previous, err
}
