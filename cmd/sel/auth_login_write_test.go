package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthLoginHasAWriteFlag(t *testing.T) {
	login, _, err := newRoot().Find([]string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	flag := login.Flags().Lookup("write")
	if flag == nil {
		t.Fatal("auth login has no --write flag")
	}
	if want := "Also allow changes (revoke tokens, and the write commands as they ship)"; flag.Usage != want {
		t.Errorf("usage = %q, want %q", flag.Usage, want)
	}
}

func TestAuthLoginWriteAsksTheServerForTheWriteScope(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/device/code" {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"device_code":"d","user_code":"ABCD-EFGH","verification_uri_complete":"http://x/device","expires_in":600,"interval":1}`))
			return
		}
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"access_denied"}`))
	}))
	t.Cleanup(srv.Close)

	res := runBinary(t, t.TempDir(), srv.URL, "auth", "login", "--setup", "--write", "--no-browser", "--json")

	if got["scope"] != "write setup" {
		t.Errorf("code request = %v, want scope \"write setup\"", got)
	}
	if res.exit != 6 {
		t.Errorf("exit = %d, want 6 after the denial", res.exit)
	}
}

func TestAuthLoginWriteAgainstAnOlderServerExitsFiveWithACodedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_scope"}`))
	}))
	t.Cleanup(srv.Close)

	res := runBinary(t, t.TempDir(), srv.URL, "auth", "login", "--write", "--no-browser", "--json")

	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	last := lines[len(lines)-1]
	if res.exit != 5 || !strings.HasPrefix(last, `{"v":1,"error":"invalid_request"`) {
		t.Errorf("exit %d last line %q, want exit 5 and error invalid_request", res.exit, last)
	}
}
