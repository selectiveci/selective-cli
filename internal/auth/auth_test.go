package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/config"
	"github.com/selectiveci/selective-cli/internal/contract"
	"github.com/selectiveci/selective-cli/internal/credentials"
	"github.com/zalando/go-keyring"
)

// fakeAPI accepts one token until it's revoked.
func fakeAPI(t *testing.T) (*httptest.Server, *bool) {
	revoked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_live_good" || revoked {
			w.WriteHeader(401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/me":
			_, _ = w.Write([]byte(`{"user_id":"u1","email":"ben@example.com","org_slugs":["acme"]}`))
		case "DELETE /api/v1/tokens/current":
			revoked = true
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &revoked
}

func setup(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	t.Setenv("SELECTIVE_TOKEN", "")
	t.Setenv("SELECTIVE_HOST", "")
}

func TestWithTokenStatusLogout(t *testing.T) {
	setup(t)
	srv, revoked := fakeAPI(t)
	var stderr, stdout bytes.Buffer

	err := Login(LoginOptions{Hostname: srv.URL, WithToken: true, Stdin: strings.NewReader("sk_live_good\n"), Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Logged in to "+srv.URL+" as ben@example.com") {
		t.Fatalf("login output: %s", stderr.String())
	}

	if err := Status("", false, &stdout); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout.String(), "Logged in as ben@example.com") || strings.Contains(stdout.String(), "sk_live_good") {
		t.Fatalf("status output leaks or misses the login: %s", stdout.String())
	}

	stderr.Reset()
	if err := Logout("", &stderr); err != nil {
		t.Fatal(err)
	}
	if !*revoked {
		t.Fatal("logout did not revoke the token on the server")
	}
	cfg, _ := config.Load()
	if _, err := credentials.Lookup(cfg, srv.URL); err == nil {
		t.Fatal("token still stored after logout")
	}

	stdout.Reset()
	if err := Status(srv.URL, false, &stdout); err == nil || !IsSilent(err) {
		t.Fatalf("status after logout should fail quietly, got %v", err)
	}
}

func TestWithTokenRejectsBadInput(t *testing.T) {
	setup(t)
	srv, _ := fakeAPI(t)
	for _, in := range []string{"", "not-a-token\n", "sk_live_wrong\n"} {
		err := Login(LoginOptions{Hostname: srv.URL, WithToken: true, Stdin: strings.NewReader(in), Stderr: &bytes.Buffer{}})
		if err == nil {
			t.Errorf("input %q should fail", in)
		}
	}
}

func TestStatusWhenTheTokenWasRevokedElsewhere(t *testing.T) {
	setup(t)
	srv, revoked := fakeAPI(t)
	_ = Login(LoginOptions{Hostname: srv.URL, WithToken: true, Stdin: strings.NewReader("sk_live_good\n"), Stderr: &bytes.Buffer{}})
	*revoked = true
	var stdout bytes.Buffer
	if err := Status("", false, &stdout); err == nil || !strings.Contains(stdout.String(), "rejected") {
		t.Fatalf("status = %v, %s", err, stdout.String())
	}
}

func TestLogoutRefusesWithAnEnvToken(t *testing.T) {
	setup(t)
	t.Setenv("SELECTIVE_TOKEN", "sk_live_env")
	if err := Logout("", &bytes.Buffer{}); err == nil {
		t.Fatal("logout should refuse to act on SELECTIVE_TOKEN")
	}
}

func TestJSONLoginErrorEventCarriesTheContractCode(t *testing.T) {
	cases := []struct {
		name        string
		tokenStatus int
		tokenBody   string
		code        string
	}{
		{"denied", 400, `{"error":"access_denied"}`, "approval_denied"},
		{"expired", 400, `{"error":"expired_token"}`, "approval_expired"},
		{"server fault", 500, `{"error":"server_error"}`, "server_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/device/code":
					_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1}`))
				case "/api/v1/device/token":
					w.WriteHeader(tc.tokenStatus)
					_, _ = w.Write([]byte(tc.tokenBody))
				}
			}))
			t.Cleanup(srv.Close)

			out := &bytes.Buffer{}
			err := Login(LoginOptions{Hostname: srv.URL, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}})
			if err == nil {
				t.Fatal("login succeeded")
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[1], `{"v":1,"event":"error"`) || !strings.Contains(lines[1], `"error":"`+tc.code+`"`) {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
}

func TestJSONLoginEventsAllCarryTheContractVersion(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1}`))
		case "/api/v1/device/token":
			_, _ = w.Write([]byte(`{"access_token":"sk_live_test","user":{"email":"ben@example.com","org_slugs":["acme"]}}`))
		}
	}))
	t.Cleanup(srv.Close)

	out := &bytes.Buffer{}
	if err := Login(LoginOptions{Hostname: srv.URL, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want an authorize and a logged_in event", out.String())
	}
	for i, event := range []string{"authorize", "logged_in"} {
		if !strings.HasPrefix(lines[i], `{"v":1,"event":"`+event+`"`) {
			t.Errorf("line %d = %q, want an object starting {\"v\":1,\"event\":%q", i, lines[i], event)
		}
	}
}

func TestSignupAsksForTheSignupPageAndSetup(t *testing.T) {
	setup(t)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH&signup=1","expires_in":600,"interval":1}`))
		case "/api/v1/device/token":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"access_denied"}`))
		}
	}))
	t.Cleanup(srv.Close)

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	_ = Login(LoginOptions{Hostname: srv.URL, Signup: true, JSON: true, NoBrowser: true, Stdout: stdout, Stderr: stderr})
	if !strings.Contains(stdout.String(), `"url":"http://x/device?code=ABCD-EFGH&signup=1"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if got["intent"] != "signup" || got["scope"] != "setup" {
		t.Fatalf("code request = %v", got)
	}
	if !strings.Contains(stderr.String(), "create your Selective account") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func codeOf(err error) contract.Code {
	var coded contract.Coded
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func TestLoginAndLogoutRefusalsCarryTheirContractCode(t *testing.T) {
	setup(t)
	srv, _ := fakeAPI(t)
	login := func(stdin string) error {
		return Login(LoginOptions{Hostname: srv.URL, WithToken: true, Stdin: strings.NewReader(stdin), Stderr: &bytes.Buffer{}})
	}
	t.Setenv("SELECTIVE_TOKEN", "sk_live_env")

	cases := []struct {
		name string
		err  error
		want contract.Code
	}{
		{"no token on stdin", login(""), contract.UsageError},
		{"stdin is not a token", login("not-a-token\n"), contract.UsageError},
		{"server rejects the token", login("sk_live_wrong\n"), contract.Unauthorized},
		{"logout with an env token", Logout("", &bytes.Buffer{}), contract.UsageError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(tc.err); got != tc.want {
				t.Errorf("code = %q, want %q (err: %v)", got, tc.want, tc.err)
			}
		})
	}
}

func TestLoggedInEventNamesTheHostAndUserAndNeverCarriesTheToken(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1}`))
		case "/api/v1/device/token":
			_, _ = w.Write([]byte(`{"access_token":"sk_live_secret_value","user":{"email":"ben@example.com"}}`))
		}
	}))
	t.Cleanup(srv.Close)

	out := &bytes.Buffer{}
	if err := Login(LoginOptions{Hostname: srv.URL, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}

	loggedIn := strings.Split(strings.TrimSpace(out.String()), "\n")[1]
	if !strings.Contains(loggedIn, `"host":"`+srv.URL+`"`) || !strings.Contains(loggedIn, `"email":"ben@example.com"`) {
		t.Errorf("logged_in = %q, want the host and email", loggedIn)
	}
	if strings.Contains(out.String(), "sk_live_secret_value") {
		t.Errorf("stdout carries the token: %q", out.String())
	}
}
