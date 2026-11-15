package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestACommandAgainstAServerThatStaysDownReportsThreeAttemptsAndExitsFour(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"server_error","message":"down for maintenance"}`))
	}))
	t.Cleanup(srv.Close)

	res := runBinary(t, t.TempDir(), srv.URL, "orgs", "--json")

	want := `{"v":1,"error":"server_error","message":"down for maintenance","attempts":3}` + "\n"
	if res.stdout != want || res.exit != 4 || hits.Load() != 3 {
		t.Errorf("stdout = %q exit = %d after %d requests, want %q, 4 and 3", res.stdout, res.exit, hits.Load(), want)
	}
}
