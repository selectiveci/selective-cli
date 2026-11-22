package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func echoingServer(t *testing.T, message string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": "validation_failed", "message": message,
			"detail": map[string]any{"list": []any{"again " + message}, "attempts": 3},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func requireNoTokenAndRedacted(t *testing.T, token string, outputs ...string) {
	t.Helper()
	escaped, _ := json.Marshal(token)
	for _, out := range outputs {
		if strings.Contains(out, token) || strings.Contains(out, string(escaped[1:len(escaped)-1])) || !strings.Contains(out, "[redacted]") {
			t.Errorf("output %.300q must hold [redacted] and no token", out)
		}
	}
}

var showApproval = []string{"approvals", "show", approvalUUID, "--json"}

func TestServerErrorsNeverEchoTheCallersTokenFromAnySource(t *testing.T) {
	srv := echoingServer(t, "bad "+storedToken)
	cases := []struct {
		name string
		run  selRun
		args []string
	}{
		{"env", selRun{token: storedToken}, showApproval},
		{"env with --timeout", selRun{token: storedToken}, append(showApproval, "--timeout=30s")},
		{"config file", selRun{configDir: storeInConfigFile(t, srv.URL)}, showApproval},
		{"config file with --timeout", selRun{configDir: storeInConfigFile(t, srv.URL)}, append(showApproval, "--timeout=30s")},
		{"stdin with --with-token", selRun{stdin: storedToken + "\n"}, []string{"auth", "login", "--with-token", "--json"}},
		{"legacy auth --token", selRun{}, []string{"auth", "--token", storedToken, "--json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run.host = srv.URL

			res := tc.run.run(t, tc.args...)

			requireNoTokenAndRedacted(t, storedToken, res.stdout, res.stderr)
			if res.exit != 5 || !strings.Contains(res.stdout, `"error":"validation_failed"`) {
				t.Errorf("exit %d stdout %.200q, want exit 5 and the error code kept", res.exit, res.stdout)
			}
		})
	}
}

func TestServerErrorsNeverEchoAKeychainToken(t *testing.T) {
	srv := echoingServer(t, "bad "+storedToken)
	keyring.MockInit()
	_ = keyring.Set("selective-cli", srv.URL, storedToken)
	t.Setenv("SELECTIVE_HOST", srv.URL)
	t.Setenv("SELECTIVE_TOKEN", "")
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())

	code, stdout, stderr := execute(showApproval...)

	requireNoTokenAndRedacted(t, storedToken, stdout, stderr)
	if code != 5 {
		t.Errorf("exit %d, want 5", code)
	}
}

func TestLogoutNeverPrintsTheTokenInARevokeFailure(t *testing.T) {
	srv := echoingServer(t, "bad "+storedToken)

	res := selRun{host: srv.URL, configDir: storeInConfigFile(t, srv.URL)}.run(t, "auth", "logout")

	requireNoTokenAndRedacted(t, storedToken, res.stderr)
}

func TestNestedAndJSONEscapedTokensAreRedactedAndOtherFieldsSurvive(t *testing.T) {
	token := `sk_live_q"u\o<te`
	srv := echoingServer(t, "bad "+token)

	res := selRun{host: srv.URL, token: token}.run(t, showApproval...)

	var object struct {
		Message string
		Detail  struct {
			List     []string
			Attempts int
		}
	}
	err := json.Unmarshal([]byte(res.stdout), &object)
	if err != nil || object.Message != "bad [redacted]" || object.Detail.List[0] != "again bad [redacted]" || object.Detail.Attempts != 3 || res.exit != 5 {
		t.Errorf("exit %d (%v) object %+v stdout %.300q", res.exit, err, object, res.stdout)
	}
	requireNoTokenAndRedacted(t, token, res.stderr)
}

func TestTokenStraddlingTheTimeoutCutIsRedactedBeforeTheCut(t *testing.T) {
	t.Setenv("SELECTIVE_TOKEN", storedToken)
	t.Setenv("SELECTIVE_CONFIG_DIR", t.TempDir())
	for pad := 400; pad < 500; pad++ {
		srv := echoingServer(t, strings.Repeat("m", pad)+storedToken)
		t.Setenv("SELECTIVE_HOST", srv.URL)
		var stdout, stderr bytes.Buffer

		runCommand(newRoot(), append(append([]string{}, showApproval...), "--timeout=30s"), &stdout, &stderr)

		oneObjectUnder512(t, stdout.String())
		for i := 0; i+8 <= len(storedToken); i++ {
			if strings.Contains(stdout.String(), storedToken[i:i+8]) {
				t.Fatalf("pad %d: stdout holds %q of the token: %.520q", pad, storedToken[i:i+8], stdout.String())
			}
		}
	}
}

func TestErrorsWithoutTheTokenAreUnchanged(t *testing.T) {
	srv := echoingServer(t, "nope")

	res := selRun{host: srv.URL, token: storedToken}.run(t, showApproval...)

	want := `{"v":1,"error":"validation_failed","message":"nope","detail":{"attempts":3,"list":["again nope"]}}` + "\n"
	if res.exit != 5 || res.stdout != want || res.stderr != "Error: nope\n" {
		t.Errorf("exit %d stdout %q stderr %q", res.exit, res.stdout, res.stderr)
	}
}
