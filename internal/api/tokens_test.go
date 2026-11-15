package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

const (
	agentTokenJSON = `{"id":"2","name":"ci","prefix":"sel_ab12","kind":"agent","scopes":["read","write"],"organization":"acme","repos":["acme/web"],"expires_at":"2026-12-01T00:00:00Z","source":"api","inserted_at":"2026-11-01T09:30:00Z"}`
	userTokenJSON  = `{"id":"1","name":"laptop","prefix":"sel_cd34","kind":"user","scopes":["read"],"organization":null,"repos":null,"expires_at":null,"source":"cli","inserted_at":"2026-10-01T09:30:00Z"}`
)

func currentTokenServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tokens/current" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCurrentTokenDecodesTheTokenObject(t *testing.T) {
	srv := currentTokenServer(t, `{"token":`+agentTokenJSON+`}`)

	token, err := New(srv.URL, "tok").CurrentToken()

	if err != nil {
		t.Fatal(err)
	}
	if token.Prefix != "sel_ab12" || token.Kind != "agent" || *token.Organization != "acme" || token.Repos[0] != "acme/web" {
		t.Errorf("token = %+v", token)
	}
}

func TestCurrentTokenKeepsNullScopeFieldsNull(t *testing.T) {
	srv := currentTokenServer(t, `{"token":`+userTokenJSON+`}`)

	token, err := New(srv.URL, "tok").CurrentToken()

	if err != nil {
		t.Fatal(err)
	}
	if token.Organization != nil || token.Repos != nil || token.ExpiresAt != nil {
		t.Errorf("token = %+v, want null organization, repos and expires_at", token)
	}
}

func TestTokensFollowsNextCursorUntilNull(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"tokens":[`+userTokenJSON+`],"next_cursor":"after-1"}`)
		case "after-1":
			fmt.Fprint(w, `{"tokens":[`+agentTokenJSON+`],"next_cursor":null}`)
		default:
			http.Error(w, "unexpected cursor", http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)

	page, err := New(srv.URL, "tok").Tokens()

	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tokens) != 2 || page.Tokens[0].Name != "laptop" || page.Tokens[1].Name != "ci" || page.NextCursor != nil || page.Notices != nil {
		t.Errorf("page = %+v", page)
	}
	if want := []string{"limit=200", "cursor=after-1&limit=200"}; len(queries) != 2 || queries[0] != want[0] || queries[1] != want[1] {
		t.Errorf("queries = %v, want %v", queries, want)
	}
}

func TestTokensStopsAtTheRequestCeilingAndKeepsTheCursor(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		fmt.Fprint(w, `{"tokens":[`+userTokenJSON+`],"next_cursor":"`+strconv.Itoa(int(n))+`"}`)
	}))
	t.Cleanup(srv.Close)

	page, err := New(srv.URL, "tok").Tokens()

	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != maxTokenRequests || page.NextCursor == nil || len(page.Notices) != 1 || page.Notices[0] != paginationStoppedEarly {
		t.Errorf("requests %d, next_cursor %v, notices %v", requests.Load(), page.NextCursor, page.Notices)
	}
}

func TestRevokeTokenSendsDeleteWithDryRunOnlyWhenAsked(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		fmt.Fprint(w, `{"token":{"id":"a/b","name":"ci","prefix":"sel_ab12"},"result":"deleted","dry_run":false}`)
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, "tok")

	real, err := client.RevokeToken("a/b", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RevokeToken("a/b", true); err != nil {
		t.Fatal(err)
	}

	if want := []string{"DELETE /api/v1/tokens/a%2Fb", "DELETE /api/v1/tokens/a%2Fb?dry_run=true"}; len(requests) != 2 || requests[0] != want[0] || requests[1] != want[1] {
		t.Errorf("requests = %v, want %v", requests, want)
	}
	if real.Token.Name != "ci" || real.Token.Prefix != "sel_ab12" || real.Result != "deleted" || real.DryRun {
		t.Errorf("revocation = %+v", real)
	}
}
