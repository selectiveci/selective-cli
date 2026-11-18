package doctor

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/selectiveci/selective-cli/internal/api"
	"github.com/selectiveci/selective-cli/internal/contract"
)

func silentHost(t *testing.T, hangFrom int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	go func() {
		for n := 1; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if n < hangFrom {
				c.Close()
				continue
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	return "http://" + ln.Addr().String()
}

func topLevelAttempts(r *Report) string {
	fields := r.Err().(contract.Fielded).ErrorFields()
	return fmt.Sprint(fields["attempts"])
}

func TestASilentHostOutlastingTheRunTimeoutIsATimeoutThatReportsThreeAttempts(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	saved := httpTimeout
	httpTimeout = 5 * time.Second
	t.Cleanup(func() { httpTimeout = saved })
	host := silentHost(t, 3)

	began := time.Now()
	report := run(t, host, Options{Timeout: 400 * time.Millisecond})
	elapsed := time.Since(began)

	server := check(t, report, "server")
	if server.Code != contract.Timeout || exitOf(report) != 8 {
		t.Errorf("server = %+v, exit %d, want timeout and exit 8", server, exitOf(report))
	}
	if got := fmt.Sprint(server.Detail["attempts"]); got != "3" {
		t.Errorf("server detail attempts = %s, want 3", got)
	}
	if got := topLevelAttempts(report); got != "3" {
		t.Errorf("error attempts = %s, want 3", got)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %s with a 400ms timeout", elapsed)
	}
}

func TestASilentHostWithATimeoutLongerThanTheRetriesIsANetworkErrorThatReportsThreeAttempts(t *testing.T) {
	t.Cleanup(api.SetClock(&fakeClock{}))
	saved := httpTimeout
	httpTimeout = 50 * time.Millisecond
	t.Cleanup(func() { httpTimeout = saved })

	report := run(t, silentHost(t, 1), Options{Timeout: time.Minute})

	server := check(t, report, "server")
	if server.Code != contract.NetworkError || exitOf(report) != 4 {
		t.Errorf("server = %+v, exit %d, want network_error and exit 4", server, exitOf(report))
	}
	if got := fmt.Sprint(server.Detail["attempts"]); got != "3" {
		t.Errorf("server detail attempts = %s, want 3", got)
	}
	if got := topLevelAttempts(report); got != "3" {
		t.Errorf("error attempts = %s, want 3", got)
	}
}

func TestAFirstAttemptFailureCarriesNoAttemptsAnywhere(t *testing.T) {
	report := run(t, serve(t, with("/api/v1/me", reply(401, `{"error":"unauthorized","message":"x"}`))), Options{})

	if got := topLevelAttempts(report); got != "<nil>" {
		t.Errorf("error attempts = %s, want none", got)
	}
	for _, c := range report.Checks {
		if _, ok := c.Detail["attempts"]; ok {
			t.Errorf("%s detail carries attempts: %+v", c.Name, c.Detail)
		}
	}
}
