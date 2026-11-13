package devicelogin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeServer answers token polls from a script of responses.
func fakeServer(t *testing.T, script []string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	polls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/device/code":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["client_name"] == "" {
				t.Error("client_name not sent")
			}
			_, _ = w.Write([]byte(`{"device_code":"dc_x","user_code":"BCDF-GHJK","verification_uri":"http://x/device","verification_uri_complete":"http://x/device?code=BCDF-GHJK","expires_in":600,"interval":5}`))
		case "/api/v1/device/token":
			mu.Lock()
			defer mu.Unlock()
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			polls = append(polls, body["device_code"])
			if body["wait"] == "" {
				t.Error("poll didn't ask the server to hold it")
			}
			next := script[0]
			if len(script) > 1 {
				script = script[1:]
			}
			if next == "ok" {
				_, _ = w.Write([]byte(`{"access_token":"sk_live_new","token_type":"bearer","user":{"email":"ben@example.com","org_slugs":["acme"]}}`))
				return
			}
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"` + next + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &polls
}

func client(url string, slept *[]time.Duration) *Client {
	c := New(url)
	fixed := time.Unix(0, 0)
	c.Now = func() time.Time { return fixed }
	c.Sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return c
}

func TestApprovedAfterPendingAndSlowDown(t *testing.T) {
	srv, polls := fakeServer(t, []string{"authorization_pending", "slow_down", "authorization_pending", "ok"})
	var slept []time.Duration
	c := client(srv.URL, &slept)

	code, err := c.Start("selective-cli test on box", "")
	if err != nil || code.UserCode != "BCDF-GHJK" {
		t.Fatalf("Start = %+v, %v", code, err)
	}
	grant, err := c.Wait(context.Background(), code)
	if err != nil || grant.AccessToken != "sk_live_new" || grant.User.Email != "ben@example.com" {
		t.Fatalf("Wait = %+v, %v", grant, err)
	}
	if len(*polls) != 4 || (*polls)[0] != "dc_x" {
		t.Fatalf("polls = %v", *polls)
	}
	// The first poll is immediate; an instant "pending" waits out the 5s
	// interval, and slow_down adds 5s for good.
	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("slept = %v, want %v", slept, want)
	}
	for i, d := range want {
		if slept[i] != d {
			t.Fatalf("sleep %d = %v, want %v (all: %v)", i, slept[i], d, slept)
		}
	}
}

func TestDeniedAndExpired(t *testing.T) {
	for reply, want := range map[string]error{"access_denied": ErrDenied, "expired_token": ErrExpired} {
		srv, _ := fakeServer(t, []string{"authorization_pending", reply})
		var slept []time.Duration
		c := client(srv.URL, &slept)
		code, _ := c.Start("x", "")
		if _, err := c.Wait(context.Background(), code); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", reply, err, want)
		}
	}
}

func TestCancel(t *testing.T) {
	srv, _ := fakeServer(t, []string{"authorization_pending"})
	c := New(srv.URL)
	code, _ := c.Start("x", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Wait(ctx, code); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestHeldPollsDontSleep(t *testing.T) {
	srv, polls := fakeServer(t, []string{"authorization_pending", "authorization_pending", "ok"})
	var slept []time.Duration
	c := client(srv.URL, &slept)
	// The server holds each poll: time moves on 25s between readings.
	now := time.Unix(0, 0)
	c.Now = func() time.Time {
		now = now.Add(25 * time.Second)
		return now
	}
	code, _ := c.Start("x", "setup")
	if _, err := c.Wait(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	if len(*polls) != 3 || len(slept) != 0 {
		t.Fatalf("polls = %d, slept = %v", len(*polls), slept)
	}
}
