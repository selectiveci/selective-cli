package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func serve(t *testing.T, status int, headers map[string]string, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok")
}

func TestServerErrorsBecomeTypedErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		code    contract.Code
		exit    int
		message string
	}{
		{"no build", 404, nil, `{"error":"no_build","message":"No build."}`, contract.NoBuild, 3, "No build."},
		{"missing write scope uses the server's message", 403, nil, `{"error":"insufficient_scope","message":"Needs write.","scope":"write"}`, contract.InsufficientScope, 6, "Needs write."},
		{"missing setup scope keeps the login hint", 403, nil, `{"error":"insufficient_scope","message":"Needs setup.","scope":"setup"}`, contract.InsufficientScope, 6, `this login can't do that (it needs the "setup" scope); run: sel auth login --setup`},
		{"unregistered code uses the status", 409, nil, `{"error":"brand_new","message":"Nope."}`, "brand_new", 5, "Nope."},
		{"proxy HTML", 502, nil, `<html>Bad Gateway</html>`, contract.ServerError, 4, "server refused: server_error (HTTP 502)"},
		{"rate limit", 429, map[string]string{"Retry-After": "7"}, `{"error":"rate_limited","message":"Slow down."}`, contract.RateLimited, 4, "Slow down."},
		{"unexpected 204", 204, nil, ``, contract.InternalError, 9, "server refused: internal_error (HTTP 204)"},
		{"unexpected redirect", 302, nil, ``, contract.InternalError, 9, "server refused: internal_error (HTTP 302)"},
		{"4xx outside the registry", 418, nil, ``, contract.InternalError, 9, "server refused: internal_error (HTTP 418)"},
		{"409 without a code", 409, nil, ``, contract.InternalError, 9, "server refused: internal_error (HTTP 409)"},
		{"empty 400", 400, nil, ``, contract.InvalidRequest, 5, "server refused: invalid_request (HTTP 400)"},
		{"empty 406", 406, nil, ``, contract.InvalidRequest, 5, "server refused: invalid_request (HTTP 406)"},
		{"empty 422", 422, nil, ``, contract.InvalidRequest, 5, "server refused: invalid_request (HTTP 422)"},
		{"empty 408", 408, nil, ``, contract.NetworkError, 4, "server refused: network_error (HTTP 408)"},
		{"401 without a body", 401, nil, ``, contract.Unauthorized, 4, "server refused: unauthorized (HTTP 401)"},
		{"old-style error body", 422, nil, `{"error":"repo and sha params are required"}`, "repo and sha params are required", 5, "server refused: repo and sha params are required (HTTP 422)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serve(t, tc.status, tc.headers, tc.body).Me()

			var apiErr *Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if apiErr.Code != tc.code || apiErr.ExitCode() != tc.exit || apiErr.Error() != tc.message {
				t.Errorf("code %q exit %d message %q; want %q, %d, %q", apiErr.Code, apiErr.ExitCode(), apiErr.Error(), tc.code, tc.exit, tc.message)
			}
		})
	}
}

func TestRetryAfterBecomesAField(t *testing.T) {
	_, err := serve(t, 429, map[string]string{"Retry-After": "7"}, `{"error":"rate_limited"}`).Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Fields["retry_after_s"] != 7 {
		t.Errorf("fields = %v, want retry_after_s 7", apiErr.Fields)
	}
}

func TestUnauthorizedStillMatchesTheSentinel(t *testing.T) {
	_, err := serve(t, 401, nil, `{"error":"unauthorized","message":"no"}`).Me()

	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("errors.Is(err, ErrUnauthorized) = false for %v", err)
	}
	if errors.Is(&Error{Status: 403}, ErrUnauthorized) {
		t.Error("a 403 must not match ErrUnauthorized")
	}
}

func TestUnreachableServerIsANetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	client := New(srv.URL, "tok")
	srv.Close()

	_, err := client.Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != contract.NetworkError || apiErr.ExitCode() != 4 {
		t.Errorf("err = %v, want a network_error with exit 4", err)
	}
}

func TestAcceptedResponseBodyPassesThrough(t *testing.T) {
	client := serve(t, 202, nil, `{"user_id":"pending"}`)

	me, err := client.Me()

	if err != nil || me.UserID != "pending" {
		t.Errorf("me %+v err %v, want the 202 body decoded", me, err)
	}
}

func TestUndecodableSuccessBodyIsAServerError(t *testing.T) {
	_, err := serve(t, 200, nil, `not json`).Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != contract.ServerError {
		t.Errorf("err = %v, want server_error", err)
	}
}

func TestAResponseBodyThatTimesOutIsANetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"user":`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, "tok")
	client.HTTP.Timeout = 100 * time.Millisecond

	_, err := client.Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != contract.NetworkError || apiErr.ExitCode() != 4 {
		t.Errorf("err = %v, want network_error exit 4", err)
	}
}

func TestAMalformedSuccessBodyIsAServerError(t *testing.T) {
	_, err := serve(t, 200, nil, `<html>not json`).Me()

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != contract.ServerError {
		t.Errorf("err = %v, want server_error", err)
	}
}

func TestRequestsCarryTheCLIUserAgent(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL, "tok").Me(); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(seen, "sel/") || !strings.Contains(seen, "("+runtime.GOOS+"/"+runtime.GOARCH+")") {
		t.Errorf("User-Agent = %q, want sel/<version> (<os>/<arch>)", seen)
	}
}
