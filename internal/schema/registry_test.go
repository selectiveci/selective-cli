package schema

import (
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
