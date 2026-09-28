//go:build linux

// Package uinput creates a virtual multitouch touchscreen through the Linux
// uinput interface (used by the in-guest agent).
package uinput

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Linux input constants (input-event-codes.h, uinput.h).
const (
	evSyn = 0x00
	evKey = 0x01
	evAbs = 0x03

	synReport       = 0
	btnTouch        = 0x14a
	absMTSlot       = 0x2f
	absMTTouchMajor = 0x30
	absMTPositionX  = 0x35
	absMTPositionY  = 0x36
	absMTTrackingID = 0x39
	absMTPressure   = 0x3a
	inputPropDirect = 0x01

	uiDevCreate  = 0x5501     // _IO('U', 1)
	uiDevDestroy = 0x5502     // _IO('U', 2)
	uiSetEvbit   = 0x40045564 // _IOW('U', 100, int)
	uiSetKeybit  = 0x40045565 // _IOW('U', 101, int)
	uiSetAbsbit  = 0x40045567 // _IOW('U', 103, int)
	uiSetPropbit = 0x4004556e // _IOW('U', 110, int)

	absCnt = 0x40
)

// uinputUserDev mirrors struct uinput_user_dev (1116 bytes on all arches).
type uinputUserDev struct {
	Name         [80]byte
	Bustype      uint16
	Vendor       uint16
	Product      uint16
	Version      uint16
	FFEffectsMax uint32
	AbsMax       [absCnt]int32
	AbsMin       [absCnt]int32
	AbsFuzz      [absCnt]int32
	AbsFlat      [absCnt]int32
}

// Touchscreen is a virtual single-finger touchscreen (multitouch protocol B).
type Touchscreen struct {
	f    *os.File
	mu   sync.Mutex
	down bool
	id   int32
	w, h int
}

func ioctl(fd uintptr, req uintptr, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

// NewTouchscreen registers the device; Android picks it up as a touchscreen
// (INPUT_PROP_DIRECT) covering the natural panel of the given size.
func NewTouchscreen(name string, w, h int) (*Touchscreen, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("opening /dev/uinput: %w", err)
	}
	fd := f.Fd()
	for _, ev := range []int{evSyn, evKey, evAbs} {
		if err := ioctl(fd, uiSetEvbit, uintptr(ev)); err != nil {
			f.Close()
			return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
		}
	}
	if err := ioctl(fd, uiSetKeybit, btnTouch); err != nil {
		f.Close()
		return nil, err
	}
	for _, a := range []int{absMTSlot, absMTTouchMajor, absMTPositionX, absMTPositionY, absMTTrackingID, absMTPressure} {
		if err := ioctl(fd, uiSetAbsbit, uintptr(a)); err != nil {
			f.Close()
			return nil, fmt.Errorf("UI_SET_ABSBIT: %w", err)
		}
	}
	if err := ioctl(fd, uiSetPropbit, inputPropDirect); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_SET_PROPBIT: %w", err)
	}
	var dev uinputUserDev
	copy(dev.Name[:], name)
	dev.Bustype, dev.Vendor, dev.Product, dev.Version = 0x06, 0x1d6b, 0x0104, 1 // BUS_VIRTUAL
	dev.AbsMax[absMTSlot] = 9
	dev.AbsMax[absMTTrackingID] = 65535
	dev.AbsMax[absMTPositionX] = int32(w - 1)
	dev.AbsMax[absMTPositionY] = int32(h - 1)
	dev.AbsMax[absMTTouchMajor] = 255
	dev.AbsMax[absMTPressure] = 255
	if _, err := f.Write((*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing uinput device description: %w", err)
	}
	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("UI_DEV_CREATE: %w", err)
	}
	return &Touchscreen{f: f, w: w, h: h}, nil
}

// Close removes the device.
func (t *Touchscreen) Close() error {
	ioctl(t.f.Fd(), uiDevDestroy, 0)
	return t.f.Close()
}

func (t *Touchscreen) emit(typ, code uint16, value int32) {
	var b [24]byte // struct input_event on 64-bit: timeval(16) + type + code + value
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	t.f.Write(b[:])
}

func (t *Touchscreen) clamp(x, y int) (int32, int32) {
	return int32(max(0, min(x, t.w-1))), int32(max(0, min(y, t.h-1)))
}

// Down starts a touch at natural-panel pixel (x, y).
func (t *Touchscreen) Down(x, y int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cx, cy := t.clamp(x, y)
	if t.down {
		t.emit(evAbs, absMTPositionX, cx)
		t.emit(evAbs, absMTPositionY, cy)
		t.emit(evSyn, synReport, 0)
		return
	}
	t.id++
	t.down = true
	t.emit(evAbs, absMTSlot, 0)
	t.emit(evAbs, absMTTrackingID, t.id)
	t.emit(evAbs, absMTTouchMajor, 5)
	t.emit(evAbs, absMTPressure, 50)
	t.emit(evAbs, absMTPositionX, cx)
	t.emit(evAbs, absMTPositionY, cy)
	t.emit(evKey, btnTouch, 1)
	t.emit(evSyn, synReport, 0)
}

// Move moves the finger (no-op when it is up).
func (t *Touchscreen) Move(x, y int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.down {
		return
	}
	cx, cy := t.clamp(x, y)
	t.emit(evAbs, absMTPositionX, cx)
	t.emit(evAbs, absMTPositionY, cy)
	t.emit(evSyn, synReport, 0)
}

// Up lifts the finger.
func (t *Touchscreen) Up() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.down {
		return
	}
	t.down = false
	t.emit(evAbs, absMTTrackingID, -1)
	t.emit(evKey, btnTouch, 0)
	t.emit(evSyn, synReport, 0)
}

// Swipe performs a straight one-finger swipe over d (used for scrolling).
func (t *Touchscreen) Swipe(x0, y0, x1, y1 int, d time.Duration) {
	const steps = 8
	t.Down(x0, y0)
	for i := 1; i <= steps; i++ {
		time.Sleep(d / steps)
		t.Move(x0+(x1-x0)*i/steps, y0+(y1-y0)*i/steps)
	}
	t.Up()
}
