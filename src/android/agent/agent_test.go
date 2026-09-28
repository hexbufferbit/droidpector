package agent

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeTS struct {
	mu  sync.Mutex
	log []string
}

func (f *fakeTS) rec(s string)  { f.mu.Lock(); f.log = append(f.log, s); f.mu.Unlock() }
func (f *fakeTS) Down(x, y int) { f.rec("down") }
func (f *fakeTS) Move(x, y int) { f.rec("move") }
func (f *fakeTS) Up()           { f.rec("up") }
func (f *fakeTS) Swipe(x0, y0, x1, y1 int, d time.Duration) {
	f.rec("swipe")
}

func TestProtocolRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	ts := &fakeTS{}
	done := make(chan struct{})
	go func() { Serve(b, b, ts, 720, 1280); close(done) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, func(context.Context) (net.Conn, error) { return a, nil })
	if err != nil {
		t.Fatal(err)
	}
	if c.W != 720 || c.H != 1280 {
		t.Fatalf("size %dx%d", c.W, c.H)
	}
	c.Down(10, 20)
	c.Move(11, 21)
	c.Up()
	c.Scroll(100, 100, 0, -200)
	c.Close()
	<-done
	got := ts.log
	want := []string{"down", "move", "up", "swipe", "up"} // trailing up: finger safety on disconnect
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestBinaryPresence(t *testing.T) {
	// The embedded agent may be absent in a bare checkout; the API must not panic.
	_ = Binary("amd64")
	if Binary("nope") != nil {
		t.Fatal("unknown arch must return nil")
	}
}
