//go:build !linux

package uinput

import (
	"errors"
	"time"
)

// Touchscreen is only available on Linux (the agent runs inside the guest).
type Touchscreen struct{}

// NewTouchscreen fails outside Linux.
func NewTouchscreen(string, int, int) (*Touchscreen, error) {
	return nil, errors.New("uinput is only available on Linux")
}

func (*Touchscreen) Close() error                            { return nil }
func (*Touchscreen) Down(int, int)                           {}
func (*Touchscreen) Move(int, int)                           {}
func (*Touchscreen) Up()                                     {}
func (*Touchscreen) Swipe(int, int, int, int, time.Duration) {}
