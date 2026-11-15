package format

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrettyShowsARetriedFailureOnce(t *testing.T) {
	var out bytes.Buffer
	fw := NewWriter(&out, "pretty")
	fail := `{"event":"test_fail","id":"t1","node":"0","file":"./spec/a_spec.rb","line":16,"error":"expected 0"}`
	for range 3 {
		if err := fw.Write([]byte(fail)); err != nil {
			t.Fatal(err)
		}
	}
	got := out.String()
	if n := strings.Count(got, "expected 0"); n != 1 {
		t.Errorf("error shown %d times, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "failed again (attempt 3)") {
		t.Errorf("missing the retry line:\n%s", got)
	}
}

func TestPrettySummaryCountsFlakyTests(t *testing.T) {
	var out bytes.Buffer
	fw := NewWriter(&out, "pretty")
	if err := fw.Write([]byte(`{"event":"summary","passed":53,"failed":1,"flaky":3,"ms":54000}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3 flaky") {
		t.Errorf("summary missing flaky count: %q", out.String())
	}
}
