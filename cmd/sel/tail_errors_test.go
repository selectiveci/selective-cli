package main

import (
	"net/http"
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
