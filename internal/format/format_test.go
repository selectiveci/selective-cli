package format

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectionEventSaysWhyEveryTestRuns(t *testing.T) {
	var out bytes.Buffer
	fw := NewWriter(&out, "pretty")

	err := fw.Write([]byte(`{"event":"selection","suite":"Server","outcome":"full","applied":true,"source":"live",` +
		`"cause":{"code":"full_run_pattern","path":"db/migrate/1_add_index.rb","rule":"db/migrate/**",` +
		`"message":"db/migrate/1_add_index.rb changed, and it matches the full-run rule db/migrate/**: every test depends on it."}}`))
	if err != nil {
		t.Fatal(err)
	}

	got := out.String()
	want := "Server  running every test: db/migrate/1_add_index.rb changed"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want it to contain %q", got, want)
	}
}

func TestSelectionEventMarksAShadowMapView(t *testing.T) {
	line := selectionLine(Event{"suite": "API", "outcome": "selected", "applied": false, "source": "map"})

	if line != "API  would run only the selected tests (test map only, before the full decision)" {
		t.Fatalf("got %q", line)
	}
}

func TestNDJSONPassesSelectionEventsThrough(t *testing.T) {
	var out bytes.Buffer
	fw := NewWriter(&out, "ndjson")
	raw := `{"event":"selection","suite":"API","outcome":"selected"}`

	if err := fw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != raw {
		t.Fatalf("got %q", out.String())
	}
}
