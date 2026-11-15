package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

const selectionReply = `{"selection":{"cause":"map","map":{"id":"m1","environment":"ruby-3.3/rspec"},"selected":["./spec/a_spec.rb[1:1]"],"outcome":"select","counts":{"total":2,"selected":1,"skipped":1,"changed_files":1}}}`

type recorded struct {
	method, path, auth, accept string
	body                       map[string]any
}

func selectionServer(t *testing.T, status int, reply string) (*Client, *recorded) {
	t.Helper()
	got := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.path = r.Method, r.URL.Path
		got.auth, got.accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		_ = json.Unmarshal(raw, &got.body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok"), got
}

func TestSelectTestsPostsTheChangeAndReadsTheSelection(t *testing.T) {
	client, got := selectionServer(t, 200, selectionReply)

	sel, err := client.SelectTests("acme corp", SelectionRequest{
		SuiteID: "s1", Language: "ruby", Framework: "rspec", Prefix: "web/", Changed: []string{"web/app/a.rb"},
		TestCases: []SelectionCase{{ID: "./spec/a_spec.rb[1:1]", FilePath: "./spec/a_spec.rb"}},
	})

	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/api/v1/orgs/acme corp/selections" || got.auth != "Bearer tok" || got.accept != "application/json" {
		t.Errorf("request = %s %s auth %q accept %q", got.method, got.path, got.auth, got.accept)
	}
	want := map[string]any{
		"suite_id": "s1", "language": "ruby", "framework": "rspec", "prefix": "web/", "changed": []any{"web/app/a.rb"},
		"test_cases": []any{map[string]any{"id": "./spec/a_spec.rb[1:1]", "file_path": "./spec/a_spec.rb"}},
	}
	if !reflect.DeepEqual(got.body, want) {
		t.Errorf("body = %v, want %v", got.body, want)
	}
	if sel.Outcome != "select" || sel.Cause != "map" || sel.Map == nil || !reflect.DeepEqual(sel.Selected, []string{"./spec/a_spec.rb[1:1]"}) || sel.Counts.ChangedFiles != 1 || sel.Counts.Skipped != 1 {
		t.Errorf("selection = %+v", sel)
	}
}

func TestSelectTestsSendsAnEmptyChangeAsAListNeverNull(t *testing.T) {
	client, got := selectionServer(t, 200, selectionReply)

	if _, err := client.SelectTests("acme", SelectionRequest{SuiteID: "s1", Language: "ruby", Framework: "rspec"}); err != nil {
		t.Fatal(err)
	}

	if changed, ok := got.body["changed"].([]any); !ok || len(changed) != 0 {
		t.Errorf("changed = %#v, want []", got.body["changed"])
	}
	if cases, ok := got.body["test_cases"].([]any); !ok || len(cases) != 0 {
		t.Errorf("test_cases = %#v, want []", got.body["test_cases"])
	}
}

func TestSelectTestsRelaysAnUnknownSuiteAsExitThree(t *testing.T) {
	client, _ := selectionServer(t, 404, `{"error":"suite_not_found","message":"No such suite in this organization."}`)

	_, err := client.SelectTests("acme", SelectionRequest{SuiteID: "nope"})

	coded, ok := err.(contract.Coded)
	if !ok || coded.ErrorCode() != contract.SuiteNotFound || coded.ExitCode() != contract.ExitNotFound {
		t.Errorf("err = %v, want suite_not_found with exit 3", err)
	}
}
