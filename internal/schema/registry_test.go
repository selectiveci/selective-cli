package schema

import (
	"regexp"
	"strings"
	"testing"

	"github.com/selectiveci/selective-cli/internal/contract"
)

func TestErrorsAreExactlyTheContractRegistry(t *testing.T) {
	got := map[string]int{}
	for _, e := range Errors() {
		got[e.Code] = e.Exit
	}
	for _, code := range contract.Codes() {
		if exit, ok := got[string(code)]; !ok || exit != contract.ExitFor(code, 0) {
			t.Errorf("code %q: schema has exit %d (present=%v), contract says %d", code, exit, ok, contract.ExitFor(code, 0))
		}
	}
	if len(got) != len(contract.Codes()) {
		t.Errorf("schema has %d error codes, contract has %d", len(got), len(contract.Codes()))
	}
}

func TestEveryContractExitHasAMeaning(t *testing.T) {
	meanings := map[int]string{}
	for _, e := range ExitCodes() {
		if _, dup := meanings[e.Code]; dup {
			t.Errorf("exit %d is listed twice", e.Code)
		}
		meanings[e.Code] = e.Meaning
	}
	for _, e := range Errors() {
		if meanings[e.Exit] == "" {
			t.Errorf("code %q exits %d, which has no meaning in exit_codes", e.Code, e.Exit)
		}
	}
}

func TestOnlyExitFourIsRetryable(t *testing.T) {
	for _, e := range ExitCodes() {
		if e.Retryable != (e.Code == contract.ExitRetryable) {
			t.Errorf("exit %d retryable=%v", e.Code, e.Retryable)
		}
	}
}

func TestEventsAreUniqueAndAttributed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Events() {
		if seen[e.Name] || (e.EmittedBy != "server" && e.EmittedBy != "cli") {
			t.Errorf("event %+v is duplicated or has no source", e)
		}
		seen[e.Name] = true
	}
}

func TestAccessorsReturnCopies(t *testing.T) {
	exit, event := ExitCodes()[0], Events()[0]
	ExitCodes()[0].Meaning = "changed"
	Events()[0].Name = "changed"
	if ExitCodes()[0] != exit || Events()[0] != event {
		t.Error("writing through an accessor changed the registry")
	}
}

func TestEventsAreExactlyTheContractEventTable(t *testing.T) {
	want := []string{
		"build_start", "selection", "progress", "summary", "test_fail", "replay_truncated",
		"waiting", "heartbeat", "rerun_started", "done", "test_pass", "authorize", "logged_in",
	}
	got := map[string]bool{}
	for _, e := range Events() {
		got[e.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("event %q is in the contract table but not the registry", name)
		}
	}
	if len(got) != len(want) {
		t.Errorf("registry has %d events, contract table has %d", len(got), len(want))
	}
}

func TestExitSevenMeaningCoversPendingAndExpiredApprovals(t *testing.T) {
	for _, e := range ExitCodes() {
		if e.Code != contract.ExitPending {
			continue
		}
		for _, word := range []string{"pending", "expired"} {
			if !strings.Contains(strings.ToLower(e.Meaning), word) {
				t.Errorf("exit 7 meaning %q does not mention %q", e.Meaning, word)
			}
		}
	}
}

func TestEveryExitMeaningNamesTheCodesMappedToIt(t *testing.T) {
	codes := map[string]int{"runner_error": contract.ExitFailures, "stale_key": contract.ExitUsage}
	for _, e := range Errors() {
		codes[e.Code] = e.Exit
	}
	meanings := map[int]string{}
	for _, e := range ExitCodes() {
		meanings[e.Code] = e.Meaning
	}
	for code, exit := range codes {
		if !regexp.MustCompile(`\b` + code + `\b`).MatchString(meanings[exit]) {
			t.Errorf("exit %d meaning %q does not name %q", exit, meanings[exit], code)
		}
	}
}
