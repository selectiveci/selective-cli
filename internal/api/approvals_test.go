package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
)

const approvalWithNulls = `{"approval":{"id":"0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90","url":"https://selective.example/acme/approvals/0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90","status":"approved","action":"member.remove","subject":{"type":"member","id":"u1","label":null},"params":{"role":"user"},"requested_by":{"token_id":null,"token_name":"ci","user_id":null},"expires_at":"2026-11-14T12:00:00.5Z","decided_at":"2026-11-14T11:00:00.5Z","decided_by_user_id":"u2","decision_note":null,"consumed_at":null,"inserted_at":"2026-11-14T10:00:00.5Z"}}`

func TestGetApprovalDecodesNullableFieldsAndKeepsParamsAsSent(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, approvalWithNulls)
	}))
	t.Cleanup(srv.Close)

	a, err := New(srv.URL, "tok").GetApproval("0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90")

	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/approvals/0193b6a0-7c1e-7d3a-9f2b-4a5c6d7e8f90" {
		t.Errorf("path = %q", path)
	}
	if a.Subject.Label != nil || a.RequestedBy.TokenID != nil || a.DecisionNote != nil || a.ConsumedAt != nil {
		t.Errorf("approval = %+v, want null fields to stay nil", a)
	}
	if a.DecidedAt == nil || *a.DecidedAt != "2026-11-14T11:00:00.5Z" || string(a.Params) != `{"role":"user"}` {
		t.Errorf("decided_at %v params %s", a.DecidedAt, a.Params)
	}
}

func TestApprovalsStopsAtTheRequestCapKeepsTheCursorAndSaysSo(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"approvals":[],"next_cursor":"more"}`)
	}))
	t.Cleanup(srv.Close)

	page, err := New(srv.URL, "tok").Approvals("acme", "")

	if err != nil {
		t.Fatal(err)
	}
	if int(requests.Load()) != maxApprovalRequests || page.NextCursor == nil || *page.NextCursor != "more" || !slices.Equal(page.Notices, []string{PaginationStoppedEarly}) {
		t.Errorf("requests %d next %v notices %v, want the cap, the cursor kept and the notice", requests.Load(), page.NextCursor, page.Notices)
	}
}
