package auth

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginServer(t *testing.T) *httptest.Server {
	return deviceServer(t, 200, goodCode, `{"access_token":"sk_live_test","user":{"email":"ben@example.com","org_slugs":["acme"]}}`, nil)
}

func stubBrowser(t *testing.T, launches bool) *[]string {
	t.Helper()
	var opened []string
	prev := openBrowser
	openBrowser = func(url string) bool {
		opened = append(opened, url)
		return launches
	}
	t.Cleanup(func() { openBrowser = prev })
	return &opened
}

func authorizeEvent(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &event); err != nil || event["event"] != "authorize" {
		t.Fatalf("first stdout line is not an authorize event: %q", stdout)
	}
	return event
}

// Stdio is a buffer here, as it is a pipe in an agent's shell: the browser
// still opens, and the code and URL are still printed.
func TestLoginOpensTheBrowserWithoutATerminal(t *testing.T) {
	setup(t)
	opened := stubBrowser(t, true)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if err := Login(LoginOptions{Hostname: loginServer(t).URL, Setup: true, JSON: true, Stdout: stdout, Stderr: stderr}); err != nil {
		t.Fatal(err)
	}

	if len(*opened) != 1 || (*opened)[0] != "http://x/device?code=ABCD-EFGH" {
		t.Fatalf("opened %v, want the verification URL once", *opened)
	}
	for _, want := range []string{"ABCD-EFGH", "  http://x/device?code=ABCD-EFGH\n", "Opened it in your browser"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
	if authorizeEvent(t, stdout.String())["browser_opened"] != true {
		t.Errorf("authorize event = %s, want browser_opened true", stdout.String())
	}
}

func TestLoginFallsBackToTheURLWhenNoBrowserLaunches(t *testing.T) {
	setup(t)
	stubBrowser(t, false)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if err := Login(LoginOptions{Hostname: loginServer(t).URL, JSON: true, Stdout: stdout, Stderr: stderr}); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(stderr.String(), "Opened it") || !strings.Contains(stderr.String(), "  http://x/device?code=ABCD-EFGH\n") {
		t.Errorf("stderr = %q, want the URL and no claim of opening", stderr.String())
	}
	if authorizeEvent(t, stdout.String())["browser_opened"] != false {
		t.Errorf("authorize event = %s, want browser_opened false", stdout.String())
	}
}

func TestNoBrowserFlagNeverLaunches(t *testing.T) {
	setup(t)
	opened := stubBrowser(t, true)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	if err := Login(LoginOptions{Hostname: loginServer(t).URL, NoBrowser: true, JSON: true, Stdout: stdout, Stderr: stderr}); err != nil {
		t.Fatal(err)
	}

	if len(*opened) != 0 {
		t.Errorf("opened %v with --no-browser", *opened)
	}
	if authorizeEvent(t, stdout.String())["browser_opened"] != false {
		t.Errorf("authorize event = %s, want browser_opened false", stdout.String())
	}
}
