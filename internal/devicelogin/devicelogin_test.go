package devicelogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

// fakeServer answers token polls from a script of responses.
func fakeServer(t *testing.T, script []string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	polls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "sel/") {
			t.Errorf("%s User-Agent = %q, want sel/...", r.URL.Path, r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/api/v1/device/code":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["client_name"] == "" {
				t.Error("client_name not sent")
			}
			_, _ = w.Write([]byte(`{"device_code":"dc_x","user_code":"BCDF-GHJK","verification_uri":"http://x/device","verification_uri_complete":"http://x/device?code=BCDF-GHJK","expires_in":600,"interval":5}`))
		case "/api/v1/device/token":
			mu.Lock()
			defer mu.Unlock()
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			polls = append(polls, body["device_code"])
			if body["wait"] == "" {
				t.Error("poll didn't ask the server to hold it")
			}
			next := script[0]
			if len(script) > 1 {
				script = script[1:]
			}
			if next == "ok" {
				_, _ = w.Write([]byte(`{"access_token":"sk_live_new","token_type":"bearer","user":{"email":"ben@example.com","org_slugs":["acme"]}}`))
				return
			}
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"` + next + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

func client(url string, slept *[]time.Duration) *Client {
	c := New(url)
	fixed := time.Unix(0, 0)
	c.Now = func() time.Time { return fixed }
	c.Sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return c
}

func TestApprovedAfterPendingAndSlowDown(t *testing.T) {
	srv, polls := fakeServer(t, []string{"authorization_pending", "slow_down", "authorization_pending", "ok"})
	var slept []time.Duration
	c := client(srv.URL, &slept)

	code, err := c.Start("selective-cli test on box", "")
	if err != nil || code.UserCode != "BCDF-GHJK" {
		t.Fatalf("Start = %+v, %v", code, err)
	}
	grant, err := c.Wait(context.Background(), code)
	if err != nil || grant.AccessToken != "sk_live_new" || grant.User.Email != "ben@example.com" {
		t.Fatalf("Wait = %+v, %v", grant, err)
	}
	if len(*polls) != 4 || (*polls)[0] != "dc_x" {
		t.Fatalf("polls = %v", *polls)
	}
	// The first poll is immediate; an instant "pending" waits out the 5s
	// interval, and slow_down adds 5s for good.
	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("slept = %v, want %v", slept, want)
	}
	for i, d := range want {
		if slept[i] != d {
			t.Fatalf("sleep %d = %v, want %v (all: %v)", i, slept[i], d, slept)
		}
	}
}

func TestDeniedAndExpired(t *testing.T) {
	for reply, want := range map[string]error{"access_denied": ErrDenied, "expired_token": ErrExpired} {
		srv, _ := fakeServer(t, []string{"authorization_pending", reply})
		var slept []time.Duration
		c := client(srv.URL, &slept)
		code, _ := c.Start("x", "")
		if _, err := c.Wait(context.Background(), code); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", reply, err, want)
		}
	}
}

func codedAs(t *testing.T, err error) contract.Coded {
	t.Helper()
	var coded contract.Coded
	if !errors.As(err, &coded) {
		t.Fatalf("err = %v, want a coded error", err)
	}
	return coded
}

func TestDeniedAndExpiredCarryTheirContractCodes(t *testing.T) {
	cases := []struct {
		reply string
		code  contract.Code
		exit  int
	}{
		{"access_denied", contract.ApprovalDenied, contract.ExitForbidden},
		{"expired_token", contract.ApprovalExpired, contract.ExitPending},
	}
	for _, tc := range cases {
		t.Run(tc.reply, func(t *testing.T) {
			srv, _ := fakeServer(t, []string{"authorization_pending", tc.reply})
			var slept []time.Duration
			c := client(srv.URL, &slept)
			code, _ := c.Start("x", "")
			_, err := c.Wait(context.Background(), code)
			coded := codedAs(t, err)
			if coded.ErrorCode() != tc.code || coded.ExitCode() != tc.exit {
				t.Errorf("code %q exit %d, want %q exit %d", coded.ErrorCode(), coded.ExitCode(), tc.code, tc.exit)
			}
		})
	}
}

func TestLocalDeadlineIsApprovalExpired(t *testing.T) {
	srv, _ := fakeServer(t, []string{"authorization_pending"})
	var slept []time.Duration
	c := client(srv.URL, &slept)
	code, _ := c.Start("x", "")
	now := time.Unix(0, 0)
	c.Now = func() time.Time {
		now = now.Add(time.Duration(code.ExpiresIn+1) * time.Second)
		return now
	}
	_, err := c.Wait(context.Background(), code)
	if coded := codedAs(t, err); coded.ErrorCode() != contract.ApprovalExpired {
		t.Errorf("code %q, want approval_expired", coded.ErrorCode())
	}
}

func TestHTTPFailuresCarryTheStatusContractCode(t *testing.T) {
	const startOK = `{"device_code":"dc_x","user_code":"BCDF-GHJK","verification_uri_complete":"http://x/device","expires_in":600,"interval":5}`
	cases := []struct {
		name        string
		codeStatus  int
		codeBody    string
		tokenStatus int
		tokenBody   string
		code        contract.Code
		exit        int
	}{
		{"start 500", 500, `{"error":"server_error"}`, 0, "", contract.ServerError, contract.ExitRetryable},
		{"start 500 with no body", 500, ``, 0, "", contract.ServerError, contract.ExitRetryable},
		{"start 429", 429, `{"error":"slow_down"}`, 0, "", contract.RateLimited, contract.ExitRetryable},
		{"start 404 html", 404, `<html>not here</html>`, 0, "", contract.NotFound, contract.ExitNotFound},
		{"start 400", 400, `{"error":"invalid_request"}`, 0, "", contract.InvalidRequest, contract.ExitUsage},
		{"poll 500", 200, startOK, 500, `{"error":"server_error"}`, contract.ServerError, contract.ExitRetryable},
		{"poll 500 with no body", 200, startOK, 500, ``, contract.ServerError, contract.ExitRetryable},
		{"poll 400 invalid_grant", 200, startOK, 400, `{"error":"invalid_grant"}`, contract.InvalidRequest, contract.ExitUsage},
		{"poll 429", 200, startOK, 429, `{"error":"rate_limited"}`, contract.RateLimited, contract.ExitRetryable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status, body := tc.codeStatus, tc.codeBody
				if r.URL.Path == "/api/v1/device/token" {
					status, body = tc.tokenStatus, tc.tokenBody
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)
			var slept []time.Duration
			c := client(srv.URL, &slept)

			code, err := c.Start("x", "")
			if err == nil {
				_, err = c.Wait(context.Background(), code)
			}

			coded := codedAs(t, err)
			if coded.ErrorCode() != tc.code || coded.ExitCode() != tc.exit {
				t.Errorf("code %q exit %d, want %q exit %d (err: %v)", coded.ErrorCode(), coded.ExitCode(), tc.code, tc.exit, err)
			}
		})
	}
}

type resetBody struct{}

func (resetBody) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
}

func (resetBody) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConnectionResetMidBodyStaysANetworkError(t *testing.T) {
	c := New("http://stub.invalid")
	c.HTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(resetBody{})}, nil
	})}

	_, err := c.Start("x", "")

	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("err = %v, want a *net.OpError in the chain", err)
	}
}

func TestNonJSONClientErrorsWhilePollingEndThePollImmediately(t *testing.T) {
	cases := []struct {
		status int
		code   contract.Code
	}{
		{404, contract.NotFound},
		{429, contract.RateLimited},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/device/code" {
					_, _ = w.Write([]byte(`{"device_code":"dc_x","user_code":"BCDF-GHJK","verification_uri_complete":"http://x/device","expires_in":600,"interval":5}`))
					return
				}
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("<html>proxy page</html>"))
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			sleeps := 0
			c := client(srv.URL, new([]time.Duration))
			c.Sleep = func(ctx context.Context, _ time.Duration) error {
				sleeps++
				if sleeps >= 3 {
					cancel()
				}
				return ctx.Err()
			}
			code, _ := c.Start("x", "")

			_, err := c.Wait(ctx, code)

			coded := codedAs(t, err)
			if coded.ErrorCode() != tc.code || sleeps != 0 {
				t.Errorf("code %q after %d sleeps, want %q with none (err: %v)", coded.ErrorCode(), sleeps, tc.code, err)
			}
		})
	}
}

func TestTransportAndReadFailuresWhilePollingAreRetried(t *testing.T) {
	polls := 0
	c := New("http://stub.invalid")
	c.Now = func() time.Time { return time.Unix(0, 0) }
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	c.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/device/code" {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"device_code":"dc_x","user_code":"BCDF-GHJK","verification_uri_complete":"http://x/device","expires_in":600,"interval":5}`))}, nil
		}
		polls++
		switch polls {
		case 1:
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		case 2:
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(resetBody{})}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"access_token":"sk_live_new","user":{"email":"ben@example.com"}}`))}, nil
	})}
	code, err := c.Start("x", "")
	if err != nil {
		t.Fatal(err)
	}

	grant, err := c.Wait(context.Background(), code)

	if err != nil || grant.AccessToken != "sk_live_new" || polls != 3 || len(slept) != 2 {
		t.Fatalf("grant %+v err %v after %d polls and %d sleeps, want a grant on the third poll after two retries", grant, err, polls, len(slept))
	}
}

func TestCancel(t *testing.T) {
	srv, _ := fakeServer(t, []string{"authorization_pending"})
	c := New(srv.URL)
	code, _ := c.Start("x", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Wait(ctx, code); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestHeldPollsDontSleep(t *testing.T) {
	srv, polls := fakeServer(t, []string{"authorization_pending", "authorization_pending", "ok"})
	var slept []time.Duration
	c := client(srv.URL, &slept)
	// The server holds each poll: time moves on 25s between readings.
	now := time.Unix(0, 0)
	c.Now = func() time.Time {
		now = now.Add(25 * time.Second)
		return now
	}
	code, _ := c.Start("x", "setup")
	if _, err := c.Wait(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	if len(*polls) != 3 || len(slept) != 0 {
		t.Fatalf("polls = %d, slept = %v", len(*polls), slept)
	}
}
