package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeADBD simulates adbd restarting as root: connections made before the
// restart completes still reach the old (non-root) daemon.
type fakeRootADBD struct {
	requests  int
	restartAt int // connection number from which adbd runs as root (0 = never)
	conns     int
	dropped   []int
	loseFirst bool // the first root request is lost
	refuse    bool // production build
}

func (f *fakeRootADBD) steps() rootSteps[int] {
	return rootSteps[int]{
		request: func(int) bool {
			f.requests++
			if f.refuse {
				return true
			}
			if f.loseFirst && f.requests == 1 {
				return false
			}
			if f.restartAt == 0 {
				f.restartAt = f.conns + 3 // restarts two reconnects later
			}
			return false
		},
		drop:     func(c int) { f.dropped = append(f.dropped, c) },
		connect:  func() (int, error) { f.conns++; return f.conns, nil },
		isRoot:   func(c int) bool { return f.restartAt != 0 && c >= f.restartAt },
		interval: time.Millisecond, rerequest: 20 * time.Millisecond, giveUp: time.Second,
	}
}

func TestBecomeRootWaitsForTheRestartedDaemon(t *testing.T) {
	f := &fakeRootADBD{}
	got, err := becomeRoot(context.Background(), 0, f.steps())
	if err != nil {
		t.Fatal(err)
	}
	// Connections 1 and 2 still reach the old adbd and are dropped.
	if got != 3 || f.requests != 1 || len(f.dropped) != 3 {
		t.Fatalf("got conn %d after %d requests, dropped %v", got, f.requests, f.dropped)
	}
}

func TestBecomeRootRepeatsALostRequest(t *testing.T) {
	f := &fakeRootADBD{loseFirst: true}
	st := f.steps()
	st.interval = 5 * time.Millisecond
	got, err := becomeRoot(context.Background(), 0, st)
	if err != nil {
		t.Fatal(err)
	}
	if f.requests < 2 || !st.isRoot(got) {
		t.Fatalf("requests %d, conn %d", f.requests, got)
	}
}

func TestBecomeRootGivesUpWithoutRoot(t *testing.T) {
	f := &fakeRootADBD{}
	st := f.steps()
	st.isRoot = func(int) bool { return false } // production build: never root
	st.giveUp = 30 * time.Millisecond
	got, err := becomeRoot(context.Background(), 0, st)
	if err != nil || got == 0 {
		t.Fatalf("expected the last connection without root, got %d %v", got, err)
	}
	if last := f.dropped[len(f.dropped)-1]; last == got {
		t.Fatal("the returned connection must not have been closed")
	}
}

func TestBecomeRootKeepsTheConnectionWhenRefused(t *testing.T) {
	f := &fakeRootADBD{refuse: true}
	got, err := becomeRoot(context.Background(), 7, f.steps())
	if err != nil || got != 7 || f.conns != 0 || len(f.dropped) != 0 {
		t.Fatalf("got %d %v, %d reconnects, dropped %v", got, err, f.conns, f.dropped)
	}
}

func TestBecomeRootStopsOnConnectErrorAndCancel(t *testing.T) {
	f := &fakeRootADBD{}
	st := f.steps()
	boom := errors.New("vm stopped")
	st.connect = func() (int, error) { return 0, boom }
	if _, err := becomeRoot(context.Background(), 0, st); !errors.Is(err, boom) {
		t.Fatalf("connect error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := becomeRoot(ctx, 0, f.steps()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestShutdownAbortsStartContexts(t *testing.T) {
	s := &Sandbox{}
	s.life, s.endLife = context.WithCancel(context.Background())
	done, end := s.withLife(context.Background())
	done2, end2 := s.withLife(context.Background())
	end2() // a finished start releases its context without affecting others
	if done2.Err() == nil || done.Err() != nil {
		t.Fatal("ending one start must not cancel another")
	}
	s.Shutdown()
	select {
	case <-done.Done():
	case <-time.After(time.Second):
		t.Fatal("Shutdown must cancel a start in progress")
	}
	end()
	if late, endLate := s.withLife(context.Background()); late.Err() == nil {
		endLate()
		t.Fatal("after Shutdown no new start may begin")
	}
}
