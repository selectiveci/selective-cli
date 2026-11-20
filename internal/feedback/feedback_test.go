package feedback

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

func TestReadNormalisesAndBoundsTheText(t *testing.T) {
	cases := map[string]struct {
		arg    string
		hasArg bool
		stdin  string
		want   string
	}{
		"argument is trimmed":     {"  hi  ", true, "ignored", "hi"},
		"stdin loses its newline": {"", false, "hi\n", "hi"},
		"exactly the cap":         {strings.Repeat("a", MaxBytes), true, "", strings.Repeat("a", MaxBytes)},
		"empty argument":          {"", true, "", ""},
		"blank stdin":             {"", false, "\n \t", ""},
		"one byte over":           {strings.Repeat("a", MaxBytes+1), true, "", ""},
		"bytes, not runes":        {strings.Repeat("é", MaxBytes/2+1), true, "", ""},
		"huge stdin":              {"", false, strings.Repeat("a", maxStdin+1), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Read(tc.arg, tc.hasArg, strings.NewReader(tc.stdin))

			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Errorf("Read = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestSendTextModeIsOneLineOnStdoutAndNothingElse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"feedback":{"id":"f1"},"result":"created","dry_run":false}`))
	}))
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	env := &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}

	if err := Send(env, "hi", Options{Org: "acme", Key: "k"}); err != nil {
		t.Fatal(err)
	}

	if out.String() != "Thanks, your feedback was sent to the Selective team.\n" || errOut.Len() != 0 {
		t.Errorf("stdout %q stderr %q, want one thanks line and nothing on stderr", out.String(), errOut.String())
	}
}
