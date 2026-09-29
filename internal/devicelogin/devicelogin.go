// Package devicelogin implements the client side of the OAuth 2.0 device
// authorization grant (RFC 8628) against Selective, the flow behind
// `selective auth login`.
package devicelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	ErrDenied  = errors.New("the login was denied in the browser")
	ErrExpired = errors.New("the code expired before it was approved; run selective auth login again")
)

// Code is what the server hands out to start a login.
type Code struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Grant is what a successful poll returns.
type Grant struct {
	AccessToken string `json:"access_token"`
	User        struct {
		Email    string   `json:"email"`
		OrgSlugs []string `json:"org_slugs"`
	} `json:"user"`
}

type Client struct {
	Host string
	HTTP *http.Client
	// Sleep is swapped out in tests.
	Sleep func(context.Context, time.Duration) error
}

func New(host string) *Client {
	return &Client{Host: host, HTTP: &http.Client{Timeout: 30 * time.Second}, Sleep: sleep}
}

// Start asks the server for a device code. clientName is shown on the
// approval page ("selective-cli 0.3.0 on ben-mbp") so the user can tell
// their own login from someone else's.
func (c *Client) Start(clientName string) (*Code, error) {
	var code Code
	status, err := c.post("/api/v1/device/code", map[string]string{"client_name": clientName}, &code)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("could not start login: server returned %d", status)
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return &code, nil
}

// Wait polls until the login is approved, denied or expires.
func (c *Client) Wait(ctx context.Context, code *Code) (*Grant, error) {
	interval := time.Duration(code.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	for {
		if err := c.Sleep(ctx, interval); err != nil {
			return nil, err
		}
		if code.ExpiresIn > 0 && time.Now().After(deadline) {
			return nil, ErrExpired
		}

		var body struct {
			Grant
			Error string `json:"error"`
		}
		status, err := c.post("/api/v1/device/token", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": code.DeviceCode,
		}, &body)
		if err != nil {
			// A dropped connection mid-wait isn't fatal; try again next tick.
			continue
		}
		if status == http.StatusOK && body.AccessToken != "" {
			return &body.Grant, nil
		}

		switch body.Error {
		case "authorization_pending":
		case "slow_down":
			// RFC 8628 §3.5: add 5 seconds, for good.
			interval += 5 * time.Second
		case "access_denied":
			return nil, ErrDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			return nil, fmt.Errorf("login failed: %s (HTTP %d)", orUnknown(body.Error), status)
		}
	}
}

func (c *Client) post(path string, payload any, out any) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", c.Host+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach %s: %w", c.Host, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && resp.StatusCode < 500 {
		return resp.StatusCode, fmt.Errorf("unexpected response from %s: %w", path, err)
	}
	return resp.StatusCode, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown error"
	}
	return s
}
