package tail

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	prettyStart = "\n  Build b  commit abc1234\n\n"
	prettyFail  = "  ✗  n1  a_test.rb:3\n    boom\n"
)

type build struct {
	name         string
	live         []string
	livePretty   string
	counts       string
	countsPretty string
	exit         int
}

var builds = []build{
	{"clean", nil, "", `"passed":10,"failed":0,"flaky":0,"ms":2500`, "10 passed  0 failed", 0},
	{"failures", nil, "", `"passed":9,"failed":1,"flaky":0,"ms":2500`, "9 passed  1 failed", 1},
	{"flaky", nil, "", `"passed":10,"failed":0,"flaky":1,"ms":2500`, "10 passed  0 failed  1 flaky (passed on retry)", 2},
	{"a failure seen live and no failed count", []string{testFail}, prettyFail, `"passed":10,"failed":0,"flaky":0,"ms":2500`, "10 passed  0 failed", 2},
	{"failures and flaky", []string{testFail}, prettyFail, `"passed":9,"failed":1,"flaky":1,"ms":2500`, "9 passed  1 failed  1 flaky (passed on retry)", 1},
}

func (b build) frames(status string) []string {
	statusField := ""
	if status != "" {
		statusField = fmt.Sprintf(`"status":%q, `, status)
	}
	summary := fmt.Sprintf(`{ "event" : "summary", %s"extra":[1, 2], %s }`, statusField, b.counts)
	return append(append([]string{buildStart}, b.live...), summary)
}

func (b build) pretty(note string) string {
	return prettyStart + b.livePretty + "\n  ●  " + b.countsPretty + "  2.5s" + note + "\n\n"
}

func streamOutput(t *testing.T, frames []string, fmtFlag string) (int, string, error) {
	t.Helper()
	srv, _ := fakeTailServer(t, frames, false)
	var out bytes.Buffer
	code, err := streamTo(&out, context.Background(), time.Now(), srv.URL, "tok", "b", fmtFlag)
	return code, out.String(), err
}

func TestAFinishedBuildKeepsItsExitCodeAndOutputWhateverItsStatus(t *testing.T) {
	for _, status := range []string{"", "success", "failed", "archived"} {
		for _, b := range builds {
			t.Run(fmt.Sprintf("%s/%s", status, b.name), func(t *testing.T) {
				frames := b.frames(status)

				code, pretty, err := streamOutput(t, frames, "pretty")
				if err != nil || code != b.exit || pretty != b.pretty("") {
					t.Errorf("pretty: exit %d err %v out %q, want exit %d and %q", code, err, pretty, b.exit, b.pretty(""))
				}

				code, ndjson, err := streamOutput(t, frames, "ndjson")
				if err != nil || code != b.exit || ndjson != strings.Join(frames, "\n")+"\n" {
					t.Errorf("ndjson: exit %d err %v out %q, want exit %d and the frames unchanged", code, err, ndjson, b.exit)
				}
			})
		}
	}
}

func TestABuildThatDidNotFinishExitsOneWhateverItsCounts(t *testing.T) {
	for _, status := range []string{"incomplete", "cancelled"} {
		for _, b := range builds {
			t.Run(fmt.Sprintf("%s/%s", status, b.name), func(t *testing.T) {
				frames := b.frames(status)
				note := "  build did not finish: " + status

				code, pretty, err := streamOutput(t, frames, "pretty")
				if err != nil || code != 1 || pretty != b.pretty(note) {
					t.Errorf("pretty: exit %d err %v out %q, want exit 1 and %q", code, err, pretty, b.pretty(note))
				}

				code, ndjson, err := streamOutput(t, frames, "ndjson")
				if err != nil || code != 1 || ndjson != strings.Join(frames, "\n")+"\n" {
					t.Errorf("ndjson: exit %d err %v out %q, want exit 1 and the frames unchanged", code, err, ndjson)
				}
			})
		}
	}
}
