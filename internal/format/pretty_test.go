package format

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrettyWriterSaysWhenAReplayWasTruncated(t *testing.T) {
	var out bytes.Buffer

	err := NewWriter(&out, "pretty").Write([]byte(`{"v":1,"event":"replay_truncated","shown":500,"total":900}`))

	if err != nil || !strings.Contains(out.String(), "showing 500 of 900 failures") {
		t.Errorf("err=%v out=%q, want a line saying 500 of 900 failures are shown", err, out.String())
	}
}

func TestNDJSONWriterPassesAReplayTruncatedFrameThroughUnchanged(t *testing.T) {
	var out bytes.Buffer
	frame := `{"v":1,"event":"replay_truncated","shown":500,"total":900}`

	_ = NewWriter(&out, "ndjson").Write([]byte(frame))

	if out.String() != frame+"\n" {
		t.Errorf("out=%q, want the frame and a newline", out.String())
	}
}

func TestPrettyWriterRendersIterationEndAsTheCountsOfThatIteration(t *testing.T) {
	var out bytes.Buffer

	err := NewWriter(&out, "pretty").Write([]byte(`{"v":1,"event":"iteration_end","iteration":2,"passed":3,"failed":1,"pending":0,"selected":4,"skipped":0,"ms":1500}`))

	if got := out.String(); err != nil || !strings.Contains(got, "3 passed  1 failed") || !strings.Contains(got, "1.5s") || !strings.Contains(got, "iteration 2") {
		t.Errorf("err=%v out=%q, want the counts, the time and the iteration number", err, got)
	}
}

func TestPrettyWriterShowsAStillFailingTestsErrorAgainInTheNextIteration(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out, "pretty")
	fail := `{"v":1,"event":"test_fail","node":"local","id":"t::a","file":"t.py","line":3,"error":"assert 1 == 2"}`
	end := `{"v":1,"event":"iteration_end","iteration":1,"passed":0,"failed":1,"ms":10}`

	for _, frame := range []string{fail, end, fail} {
		_ = w.Write([]byte(frame))
	}

	if got := strings.Count(out.String(), "assert 1 == 2"); got != 2 {
		t.Errorf("error shown %d times in %q, want once per iteration and no retry line", got, out.String())
	}
}
