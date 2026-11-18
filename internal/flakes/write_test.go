package flakes

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/setup"
)

func TestWriteTextModePrintsOneSentenceOnStderrAndNothingOnStdout(t *testing.T) {
	const flakeBody = `{"id":"f-1","status":"quarantined","test":{"id":"t1","name":"checkout flow","path":"spec/checkout_spec.rb"}}`
	cases := []struct {
		name   string
		status int
		body   string
		opts   WriteOptions
		run    func(*setup.Env, string, WriteOptions) error
		want   string
	}{
		{"quarantine updated", 200, `{"flake":` + flakeBody + `,"result":"updated","dry_run":false}`, WriteOptions{}, Quarantine, "Quarantined flake f-1 (checkout flow).\n"},
		{"quarantine unchanged", 200, `{"flake":` + flakeBody + `,"result":"unchanged","dry_run":false}`, WriteOptions{}, Quarantine, "Flake f-1 (checkout flow) is already quarantined.\n"},
		{"unquarantine dry run", 200, `{"flake":` + flakeBody + `,"result":"updated","dry_run":true}`, WriteOptions{DryRun: true}, Unquarantine, "Dry run: would unquarantine flake f-1 (checkout flow).\n"},
		{"mark-fixed deleted", 200, `{"flake":` + flakeBody + `,"result":"deleted","dry_run":false}`, WriteOptions{}, MarkFixed, "Marked flake f-1 (checkout flow) as fixed.\n"},
		{"mark-fixed already gone", 404, `{"error":"not_found","message":"Flake not found."}`, WriteOptions{IfExists: true}, MarkFixed, "Flake f-1 is already gone.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			var out, errOut bytes.Buffer
			env := &setup.Env{Client: api.New(srv.URL, "tok"), Stdout: &out, Stderr: &errOut}

			if err := tc.run(env, "f-1", tc.opts); err != nil {
				t.Fatal(err)
			}

			if out.Len() != 0 || errOut.String() != tc.want {
				t.Errorf("stdout %q stderr %q, want only %q on stderr", out.String(), errOut.String(), tc.want)
			}
		})
	}
}
