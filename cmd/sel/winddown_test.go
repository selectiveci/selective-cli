package main

import (
	"testing"
	"time"
)

const windDownTestBudget = 200 * time.Millisecond

func blockedWrite() *writeWatch {
	writes := &writeWatch{}
	writes.started.Add(1)
	return writes
}

func cutsOff(t *testing.T, writes *writeWatch, limit time.Duration, during func()) (bool, time.Duration) {
	t.Helper()
	wound := make(chan struct{})
	result := make(chan bool, 1)
	began := time.Now()
	go func() { result <- cutsOffWindDown(wound, writes, windDownTestBudget, limit) }()
	if during != nil {
		during()
	}
	select {
	case cut := <-result:
		return cut, time.Since(began)
	case <-time.After(windDownTestBudget + backstopGrace + limit + time.Second):
		t.Fatal("the wind-down timer never decided")
	}
	return false, 0
}

func TestTheWindDownTimerCutsOffAWriteThatNeverReturns(t *testing.T) {
	t.Parallel()
	cut, took := cutsOff(t, blockedWrite(), time.Minute, nil)
	if !cut || took > windDownTestBudget+backstopGrace+time.Second {
		t.Errorf("cut=%t after %s; want a cut at about %s", cut, took, windDownTestBudget+backstopGrace)
	}
}

func TestTheWindDownTimerSparesAReaderThatRecoveredUntilTheWindDownFinishes(t *testing.T) {
	t.Parallel()
	writes := blockedWrite()
	wound := make(chan struct{})
	result := make(chan bool, 1)
	go func() { result <- cutsOffWindDown(wound, writes, windDownTestBudget, time.Minute) }()
	time.Sleep(50 * time.Millisecond)
	writes.returned.Add(1)
	select {
	case cut := <-result:
		t.Fatalf("cut=%t before the wind-down finished; want the recovered reader spared", cut)
	case <-time.After(windDownTestBudget + 2*backstopGrace):
	}
	close(wound)
	if cut := <-result; cut {
		t.Error("cut after the wind-down finished; want no cut")
	}
}

func TestTheWindDownTimerStillEndsAReaderThatOnlyDrips(t *testing.T) {
	t.Parallel()
	writes := blockedWrite()
	limit := windDownTestBudget + 3*backstopGrace
	cut, took := cutsOff(t, writes, limit, func() {
		time.Sleep(50 * time.Millisecond)
		writes.returned.Add(1)
		writes.started.Add(1)
	})
	if !cut || took < limit {
		t.Errorf("cut=%t after %s; want a cut at the hard limit of %s and not before", cut, took, limit)
	}
}
