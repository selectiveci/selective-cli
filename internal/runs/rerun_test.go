package runs

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

func TestRerunTextFormIsOneLineOnStderr(t *testing.T) {
	cases := map[string]struct {
		body   string
		dryRun bool
		want   string
	}{
		"created":   {`{"build_id":"b1","rerun":{"enqueued":2,"runs":[]},"result":"created","dry_run":false}`, false, "Queued 2 failed test(s) of build b1 for rerun.\n"},
		"unchanged": {`{"build_id":"b1","rerun":{"enqueued":0,"runs":[]},"result":"unchanged","dry_run":false}`, false, "Nothing to queue for build b1: its failed tests are already pending.\n"},
		"dry run":   {`{"build_id":"b1","rerun":{"enqueued":2,"runs":[]},"result":"created","dry_run":true}`, true, "Dry run: would queue 2 failed test(s) of build b1 for rerun.\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			var out, errOut bytes.Buffer
			env := &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}

			if err := Rerun(env, RerunOptions{Build: "b1", DryRun: tc.dryRun, Key: "k"}); err != nil {
				t.Fatal(err)
			}

			if out.Len() != 0 || errOut.String() != tc.want {
				t.Errorf("stdout %q stderr %q, want nothing and %q", out.String(), errOut.String(), tc.want)
			}
		})
	}
}
