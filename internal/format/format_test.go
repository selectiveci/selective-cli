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

func TestPrettySelectionListsTheIDsOfAListRunAndStaysSilentOtherwise(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{"a list run", `{"event":"selection","selected":2,"total":5,"ids":["tests/a.py::one","tests/a.py::two"]}`, []string{"2 of 5", "tests/a.py::one", "tests/a.py::two"}},
		{"a list run that selects nothing", `{"event":"selection","selected":0,"total":5,"ids":[]}`, []string{"0 of 5"}},
		{"a run, which has no ids", `{"event":"selection","selected":2,"total":5}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := NewWriter(&out, "pretty").Write([]byte(tc.line)); err != nil {
				t.Fatal(err)
			}
			if tc.want == nil && out.Len() != 0 {
				t.Errorf("printed %q, want nothing", out.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("printed %q, want it to contain %q", out.String(), want)
				}
			}
		})
	}
}
