package tail

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/wait"
)

func TestMain(m *testing.M) {
	api.SetClock(&fakeClock{})
	os.Exit(m.Run())
}

func TestAOneShotLookupKeepsTheClientRetries(t *testing.T) {
	unavailable := func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
	client, calls := lookupServer(t, unavailable, unavailable, found)

	id, err := findBuild(context.Background(), client, "acme/widget", "abc1234", wait.Options{})

	if err != nil || id != "b9" || calls.Load() != 3 {
		t.Errorf("id=%q err=%v after %d lookups, want b9 after 3", id, err, calls.Load())
	}
}
