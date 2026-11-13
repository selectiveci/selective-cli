// Package devicelogin implements the client side of the OAuth 2.0 device
// authorization grant (RFC 8628) against Selective, the flow behind
// `sel auth login`.
package devicelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
)

var (
	ErrDenied  = contract.Errorf(contract.ApprovalDenied, "the login was denied in the browser")
	ErrExpired = contract.Errorf(contract.ApprovalExpired, "the code expired before it was approved; run sel auth login again")
)

// Code is what the server hands out to start a login.
type Code struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Scope                   string `json:"scope"`
}

// Grant is what a successful poll returns.
type Grant struct {
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	User        struct {
		Email    string   `json:"email"`
		OrgSlugs []string `json:"org_slugs"`
	} `json:"user"`
}

// holdSeconds is how long each poll asks the server to wait for the user's
// decision before answering "still pending".
const holdSeconds = 25

type Client struct {
	Host string
	HTTP *http.Client
	// Signup asks the approval page to greet a new user with signup.
	Signup bool
	// Sleep and Now are swapped out in tests.
	Sleep func(context.Context, time.Duration) error
	Now   func() time.Time
}

func New(host string) *Client {
	return &Client{
		Host:  host,
		HTTP:  &http.Client{Timeout: (holdSeconds + 15) * time.Second},
		Sleep: sleep,
		Now:   time.Now,
	}
}

// Start asks the server for a device code. clientName is shown on the
// approval page ("selective-cli 0.3.0 on ben-mbp") so the user can tell
// their own login from someone else's. scope is "" (read only) or "setup".
func (c *Client) Start(clientName, scope string) (*Code, error) {
	var code Code
	payload := map[string]string{"client_name": clientName}
	if scope != "" {
		payload["scope"] = scope
	}
	if c.Signup {
		payload["intent"] = "signup"
	}
	status, err := c.post(context.Background(), "/api/v1/device/code", payload, &code)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, statusError(status, fmt.Sprintf("could not start login: server returned %d", status))
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return &code, nil
}

// Wait polls until the login is approved, denied or expires. Each poll asks
// the server to hold it until the user decides, so approval is seen at once;
// a server that answers straight away is polled every interval instead.
func (c *Client) Wait(ctx context.Context, code *Code) (*Grant, error) {
	interval := time.Duration(code.Interval) * time.Second
	deadline := c.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if code.ExpiresIn > 0 && c.Now().After(deadline) {
			return nil, ErrExpired
		}

		var body struct {
			Grant
			Error string `json:"error"`
		}
		started := c.Now()
		status, err := c.post(ctx, "/api/v1/device/token", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": code.DeviceCode,
			"wait":        fmt.Sprint(holdSeconds),
		}, &body)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var refusal *api.Error
			if errors.As(err, &refusal) {
				return nil, err
			}
			// A dropped connection mid-wait isn't fatal; try again next tick.
			if err := c.Sleep(ctx, interval); err != nil {
				return nil, err
			}
			continue
		}
		if status == http.StatusOK && body.AccessToken != "" {
			return &body.Grant, nil
		}

		switch body.Error {
		case "authorization_pending":
			if held := c.Now().Sub(started); held < interval {
				if err := c.Sleep(ctx, interval-held); err != nil {
					return nil, err
				}
			}
		case "slow_down":
			// RFC 8628 §3.5: add 5 seconds, for good.
			interval += 5 * time.Second
			if err := c.Sleep(ctx, interval); err != nil {
				return nil, err
			}
		case "access_denied":
			return nil, ErrDenied
		case "expired_token":
			return nil, ErrExpired
		default:
			return nil, statusError(status, fmt.Sprintf("login failed: %s (HTTP %d)", orUnknown(body.Error), status))
		}
	}
}

func (c *Client) post(ctx context.Context, path string, payload any, out any) (int, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.Host+path, bytes.NewReader(data))
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("could not read the response from %s: %w", path, err)
	}
	if err := json.Unmarshal(body, out); err != nil && resp.StatusCode < 500 {
		if resp.StatusCode < 400 {
			return resp.StatusCode, fmt.Errorf("unexpected response from %s: %w", path, err)
		}
		return resp.StatusCode, statusError(resp.StatusCode, fmt.Sprintf("unexpected response from %s: %v", path, err))
	}
	return resp.StatusCode, nil
}

func statusError(status int, message string) error {
	return &api.Error{Status: status, Code: api.CodeForStatus(status), Message: message}
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
