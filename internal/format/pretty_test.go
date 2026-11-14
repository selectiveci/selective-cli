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
