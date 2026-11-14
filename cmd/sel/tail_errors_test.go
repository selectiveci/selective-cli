package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestTailPrettyFormatKeepsStdoutFreeOfTheErrorObject(t *testing.T) {
	srv := tailServer{lookupStatus: http.StatusNotFound, lookupBody: `{"error":"no_build"}`}.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--format", "pretty")

	if res.exit != 3 || res.stdout != "" || !strings.Contains(res.stderr, "no build for repo") {
		t.Errorf("exit = %d stdout = %q stderr = %q, want exit 3, empty stdout and the message on stderr", res.exit, res.stdout, res.stderr)
	}
}

func TestTailFormatMakesJSONMeanNDJSONAndRefusesItBesidePretty(t *testing.T) {
	cases := []struct {
		asJSON    bool
		requested string
		want      string
		wantErr   bool
	}{
		{true, "", "ndjson", false},
		{true, "ndjson", "ndjson", false},
		{true, "pretty", "", true},
		{false, "pretty", "pretty", false},
		{false, "", "", false},
	}
	for _, tc := range cases {
		got, err := tailFormat(tc.asJSON, tc.requested)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("tailFormat(%v, %q) = %q, %v; want %q, error %v", tc.asJSON, tc.requested, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestTailRefusesFlagsAndArgumentsItCannotHonourWithAUsageError(t *testing.T) {
	srv := tailServer{lookupStatus: http.StatusNotFound, lookupBody: `{}`}.start(t)
	cases := map[string][]string{
		"--json with an explicit pretty": {"--json", "--format", "pretty"},
		"a stray argument":               {"--json", "5s"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			res := runBinary(t, gitRepo(t), srv.URL, append([]string{"tail"}, args...)...)

			assertRefusal(t, res, 5, "usage_error")
		})
	}
}

func TestTailKeepsAnUnusableHostAsANetworkError(t *testing.T) {
	res := runBinary(t, gitRepo(t), "   ", "tail", "--json")

	assertRefusal(t, res, 4, "network_error")
}

func TestTailTreatsAnEmptyFormatAsTheDefault(t *testing.T) {
	srv := tailServer{lookupStatus: http.StatusNotFound, lookupBody: `{"error":"no_build"}`}.start(t)

	res := runBinary(t, gitRepo(t), srv.URL, "tail", "--format=")

	assertRefusal(t, res, 3, "no_build")
}

func TestTailWithoutALoginPointsAtPlainLoginNotSetup(t *testing.T) {
	cmd := exec.Command(os.Args[0], "tail", "--json")
	cmd.Dir = gitRepo(t)
	cmd.Env = append(os.Environ(), "SELECTIVE_TEST_RUN_MAIN=1", "SELECTIVE_HOST=http://127.0.0.1:9", "SELECTIVE_TOKEN=", "SELECTIVE_CONFIG_DIR="+t.TempDir())
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()

	out := stdout.String()
	if !strings.Contains(out, `"error":"unauthorized"`) || !strings.Contains(out, "sel auth login") || strings.Contains(out, "--setup") {
		t.Errorf("stdout = %q, want unauthorized pointing at `sel auth login` without --setup", out)
	}
}

func TestTailClassifiesAHandshakeRefusalByItsStatus(t *testing.T) {
	cases := []struct {
		status int
		exit   int
		code   string
	}{
		{http.StatusUnauthorized, 4, "unauthorized"},
		{http.StatusForbidden, 4, "unauthorized"},
		{http.StatusNotFound, 3, "not_found"},
		{http.StatusBadGateway, 4, "server_error"},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			t.Cleanup(srv.Close)

			res := runBinary(t, gitRepo(t), srv.URL, "tail", "--run-id", "b", "--json")

			assertRefusal(t, res, tc.exit, tc.code)
		})
	}
}
