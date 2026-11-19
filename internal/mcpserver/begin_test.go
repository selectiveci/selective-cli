package mcpserver

import (
	"context"
	"testing"

	"github.com/selectiveci/selective-cli/internal/schema"
)

func TestBeginRefusesOnceShutdownHasBegun(t *testing.T) {
	runner := NewRunner(schema.Document{}, "unused", Exposure{})
	if _, done, err := runner.begin(context.Background()); err != nil {
		t.Fatalf("control: begin before shutdown: %v", err)
	} else {
		done()
	}
	shutdown, cancel := context.WithCancel(context.Background())
	cancel()
	if _, done, err := runner.WithShutdown(shutdown).begin(context.Background()); err == nil {
		done()
		t.Fatal("begin admitted a call after shutdown")
	}
}
