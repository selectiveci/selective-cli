package auth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func TestLoginScopeRequested(t *testing.T) {
	cases := []struct {
		name string
		opts LoginOptions
		want string
	}{
		{"nothing", LoginOptions{}, ""},
		{"setup", LoginOptions{Setup: true}, "setup"},
		{"signup", LoginOptions{Signup: true}, "setup"},
		{"write", LoginOptions{Write: true}, "write"},
		{"setup and write", LoginOptions{Setup: true, Write: true}, "write setup"},
		{"signup and write", LoginOptions{Signup: true, Write: true}, "write setup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.opts.scope(); got != tc.want {
				t.Errorf("scope() = %q, want %q", got, tc.want)
			}
		})
	}
}

func deviceServer(t *testing.T, codeStatus int, codeBody, grant string, captured *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			if captured != nil {
				_ = json.NewDecoder(r.Body).Decode(captured)
			}
			w.WriteHeader(codeStatus)
			_, _ = w.Write([]byte(codeBody))
		case "/api/v1/device/token":
			_, _ = w.Write([]byte(grant))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const goodCode = `{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device?code=ABCD-EFGH","expires_in":600,"interval":1,"scope":"read write setup"}`

func TestSetupAndWriteSendBothScopesOnTheWire(t *testing.T) {
	setup(t)
	var got map[string]string
	srv := deviceServer(t, 200, goodCode, `{"access_token":"sk_live_test","scope":"read write setup","user":{"email":"ben@example.com"}}`, &got)

	err := Login(LoginOptions{Hostname: srv.URL, Setup: true, Write: true, JSON: true, NoBrowser: true, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if got["scope"] != "write setup" {
		t.Fatalf("code request = %v, want scope \"write setup\"", got)
	}
}

func TestLoginWithoutWriteSendsNoWriteScope(t *testing.T) {
	setup(t)
	var got map[string]string
	srv := deviceServer(t, 200, goodCode, `{"access_token":"sk_live_test","scope":"read","user":{"email":"ben@example.com"}}`, &got)

	err := Login(LoginOptions{Hostname: srv.URL, JSON: true, NoBrowser: true, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := got["scope"]; present {
		t.Fatalf("code request = %v, want no scope", got)
	}
}

func TestLoggedInEventShowsTheWriteGrant(t *testing.T) {
	setup(t)
	var requested map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			_ = json.NewDecoder(r.Body).Decode(&requested)
			_, _ = w.Write([]byte(goodCode))
		case "/api/v1/device/token":
			granted, _ := json.Marshal("read " + requested["scope"])
			_, _ = w.Write([]byte(`{"access_token":"sk_live_test","scope":` + string(granted) + `,"user":{"email":"ben@example.com"}}`))
		}
	}))
	t.Cleanup(srv.Close)

	out := &bytes.Buffer{}
	if err := Login(LoginOptions{Hostname: srv.URL, Write: true, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, `{"v":1,"event":"logged_in"`) || !strings.Contains(last, `"scope":"read write"`) {
		t.Fatalf("last event = %q", last)
	}
}

func TestOlderServerRefusingTheWriteScopeIsACodedError(t *testing.T) {
	setup(t)
	srv := deviceServer(t, 400, `{"error":"invalid_scope"}`, "", nil)

	out := &bytes.Buffer{}
	err := Login(LoginOptions{Hostname: srv.URL, Write: true, JSON: true, NoBrowser: true, Stdout: out, Stderr: &bytes.Buffer{}})
	if got := codeOf(err); got != contract.InvalidRequest {
		t.Fatalf("code = %q, want %q (err: %v)", got, contract.InvalidRequest, err)
	}
	if !strings.HasPrefix(out.String(), `{"v":1,"event":"error"`) || !strings.Contains(out.String(), `"error":"invalid_request"`) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestWithTokenIgnoresWrite(t *testing.T) {
	setup(t)
	srv, _ := fakeAPI(t)
	err := Login(LoginOptions{Hostname: srv.URL, WithToken: true, Write: true, Stdin: strings.NewReader("sk_live_good\n"), Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
}
