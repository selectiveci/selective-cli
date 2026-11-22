package main

import (
	"os"
	"testing"
)

func TestEIOMeansReaderGoneAsksThePollNotTheStartupTerminalFlag(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if eioMeansReaderGone(w, terminal) {
			t.Errorf("terminal=%t: a stdout with a live reader was reported gone", terminal)
		}
		_ = r.Close()
		if !eioMeansReaderGone(w, terminal) {
			t.Errorf("terminal=%t: a stdout whose reader closed was not reported gone", terminal)
		}
		_ = w.Close()
	}
}
