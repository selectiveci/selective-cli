package format

import (
	"bytes"
	"strings"
	"testing"
)

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
