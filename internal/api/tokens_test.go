package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
