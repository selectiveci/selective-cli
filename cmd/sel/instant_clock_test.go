package main

import (
	"context"
	"time"
)

type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }

func (instantClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
