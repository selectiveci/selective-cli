package doctor

import (
	"net/http"
	"sync"

	"github.com/selectiveci/selective-cli/internal/update"
)

type advertised struct {
	mu     sync.Mutex
	latest string
	min    string
}

func (a *advertised) record(h http.Header) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if v := h.Get(update.LatestHeader); v != "" {
		a.latest = v
	}
	if v := h.Get(update.MinHeader); v != "" {
		a.min = v
	}
}

func (a *advertised) state() update.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return update.State{ServerLatestVersion: a.latest, ServerMinVersion: a.min}
}

type recordingTransport struct {
	base http.RoundTripper
	seen *advertised
}

func (t recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		t.seen.record(resp.Header)
	}
	return resp, err
}
