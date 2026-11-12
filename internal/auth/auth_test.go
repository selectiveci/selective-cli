package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/config"
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

	if err := Status("", &stdout); err != nil {
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
	if err := Status(srv.URL, &stdout); err == nil || !IsSilent(err) {
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
	if err := Status("", &stdout); err == nil || !strings.Contains(stdout.String(), "rejected") {
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

func TestJSONLoginReportsADenial(t *testing.T) {
	setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1}`))
		case "/api/v1/device/token":
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"access_denied"}`))
		}
	}))
	t.Cleanup(srv.Close)

	out := &bytes.Buffer{}
	err := Login(LoginOptions{Hostname: srv.URL, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("login succeeded")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"event":"error"`) || !strings.Contains(lines[1], `"error":"access_denied"`) {
		t.Fatalf("stdout = %q", out.String())
	}
}
