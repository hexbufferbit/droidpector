package display

import "sync"

// TouchInjector delivers touches to the guest in natural-panel pixels (the
// in-guest agent). Pointer events from the UI are translated into touch
// gestures: left button = finger, wheel = swipe.
type TouchInjector interface {
	Down(x, y int) error
	Move(x, y int) error
	Up() error
	Scroll(x, y, dx, dy int) error
}

// touchState turns a stream of pointer events into finger gestures.
type touchState struct {
	mu   sync.Mutex
	down bool
}

// scrollStep is the swipe length of one wheel notch, in panel pixels.
const scrollStep = 160

// apply converts one pointer event; returns false when the event was not a
// touch (no button held and no wheel), i.e. a plain hover.
func (t *touchState) apply(inj TouchInjector, in Input) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case in.Buttons&8 != 0: // wheel up: content moves down → finger swipes down
		return true, inj.Scroll(in.X, in.Y, 0, scrollStep)
	case in.Buttons&16 != 0: // wheel down
		return true, inj.Scroll(in.X, in.Y, 0, -scrollStep)
	case in.Buttons&1 != 0:
		if !t.down {
			t.down = true
			return true, inj.Down(in.X, in.Y)
		}
		return true, inj.Move(in.X, in.Y)
	default:
		if t.down {
			t.down = false
			return true, inj.Up()
		}
		return false, nil
	}
}

// reset lifts a finger that may still be down (display detached).
func (t *touchState) reset(inj TouchInjector) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.down && inj != nil {
		inj.Up()
	}
	t.down = false
}
