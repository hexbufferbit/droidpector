//go:build vm

package vm

import (
	"context"
	"fmt"
	"image/jpeg"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/droidpector/apkinspector/src/display"
	"github.com/droidpector/apkinspector/src/query"
)

// TestRotationMappingProbe determines empirically how a landscape-rotated
// Android maps framebuffer pointer coordinates: it taps four different
// buttons, each through a different candidate mapping, and reports which
// requests arrive. Run alone: go test -tags vm -run TestRotationMappingProbe.
func TestRotationMappingProbe(t *testing.T) {
	apkPath := env(t, "APKINSPECTOR_TESTAPP")
	h := newHarness(t, t.TempDir())
	ctx := context.Background()
	if err := h.app.Sandbox.Start(ctx, ""); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(apkPath)
	entry, err := h.app.APKs.Add(f, "TestApp.apk")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.APKs.Install(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	dev, _ := h.app.Sandbox.Device()
	if err := h.app.Sandbox.Rotate(ctx, 1); err != nil {
		t.Fatal(err)
	}
	dev.Run(ctx, "am start -S -W -n com.apkinspector.testapp/.MainActivity")
	time.Sleep(5 * time.Second)
	disp := h.app.Sandbox.Profile().EffectiveDisplay()
	fbW, fbH := disp.Width, disp.Height

	var out string
	var lw, lh int
	for i := 0; i < 30; i++ {
		out, _, _ = dev.Run(ctx, "uiautomator dump /sdcard/ui.xml >/dev/null 2>&1; cat /sdcard/ui.xml")
		if m := rootRe.FindStringSubmatch(out); m != nil {
			lw, _ = strconv.Atoi(m[1])
			lh, _ = strconv.Atoi(m[2])
		}
		if lw > lh && strings.Contains(out, `text="POST"`) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("logical size %dx%d, framebuffer %dx%d", lw, lh, fbW, fbH)
	// Save the framebuffer for visual confirmation of where the buttons are.
	sub := h.app.Streamer.Subscribe()
	deadline := time.After(10 * time.Second)
	for saved := false; !saved; {
		select {
		case msg := <-sub.C:
			if msg[0] == display.MsgRect && int(msg[5])<<8|int(msg[6]) == fbW {
				img, err := jpeg.Decode(strings.NewReader(string(msg[9:])))
				if err == nil {
					pf, _ := os.Create(os.Getenv("PROBE_OUT") + "/landscape.png")
					png.Encode(pf, img)
					pf.Close()
					saved = true
				}
			}
		case <-deadline:
			saved = true
		}
	}
	h.app.Streamer.Unsubscribe(sub)

	tap := func(x, y int, click bool) {
		seq := []display.Input{{Type: "p", X: x, Y: y}}
		if click {
			seq = append(seq, display.Input{Type: "p", X: x, Y: y, Buttons: 1}, display.Input{Type: "p", X: x, Y: y})
		}
		for _, in := range seq {
			h.app.Streamer.Send(in)
			time.Sleep(250 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
	}
	saveFrame := func(name string) {
		sub := h.app.Streamer.Subscribe()
		defer h.app.Streamer.Unsubscribe(sub)
		deadline := time.After(10 * time.Second)
		for {
			select {
			case msg := <-sub.C:
				if msg[0] == display.MsgRect && int(msg[5])<<8|int(msg[6]) == fbW {
					if img, err := jpeg.Decode(strings.NewReader(string(msg[9:]))); err == nil {
						pf, _ := os.Create(os.Getenv("PROBE_OUT") + "/" + name + ".png")
						png.Encode(pf, img)
						pf.Close()
						return
					}
				}
			case <-deadline:
				return
			}
		}
	}
	countPath := func(path string) int {
		sid := h.app.Sandbox.Status().SessionID
		p, _ := h.app.Query.Query(ctx, query.Request{SessionID: sid, Filter: "path:" + path, Limit: 500})
		return p.Total
	}
	// Developer overlay: draws the pointer position and coordinates on screen.
	dev.Run(ctx, "settings put system pointer_location 1")
	time.Sleep(2 * time.Second)
	// Raw events as the guest kernel sees them, plus the on-screen cursor for
	// five hover points; together they pin the pointer mapping exactly.
	evdev, _, _ := dev.Run(ctx, "getevent -p 2>/dev/null | grep -B1 -i tablet | grep -o '/dev/input/event[0-9]*' | head -1")
	evdev = strings.TrimSpace(evdev)
	t.Logf("tablet event device: %q", evdev)
	dev.Run(ctx, "rm -f /sdcard/ge.txt; (getevent -l "+evdev+" > /sdcard/ge.txt 2>&1 &) ; sleep 1")
	for i, pt := range [][2]int{{100, 100}, {600, 100}, {100, 1100}, {600, 1100}, {360, 640}} {
		tap(pt[0], pt[1], false)
		saveFrame(fmt.Sprintf("hover-l%d-%d-%d", i, pt[0], pt[1]))
	}
	dev.Run(ctx, "pkill getevent; sleep 1")
	ge, _, _ := dev.Run(ctx, "grep -E 'ABS_X|ABS_Y' /sdcard/ge.txt | head -40")
	t.Logf("RAW landscape events:\n%s", ge)
	dump, _, _ := dev.Run(ctx, "dumpsys input | grep -B2 -A30 'Device 3' | grep -iE 'Scale|Orientation|Viewport|logicalFrame|physicalFrame|deviceSize|X: source|Y: source' | head -20")
	t.Logf("dumpsys landscape:\n%s", dump)
	// Second pass: portrait, same procedure.
	lx, ly := 0, 0
	h.app.Sandbox.Rotate(ctx, 0)
	time.Sleep(4 * time.Second)
	dev.Run(ctx, "am start -S -W -n com.apkinspector.testapp/.MainActivity")
	time.Sleep(5 * time.Second)
	out, _, _ = dev.Run(ctx, "uiautomator dump /sdcard/ui.xml >/dev/null 2>&1; cat /sdcard/ui.xml")
	for _, m := range boundsRe.FindAllStringSubmatch(out, -1) {
		if m[1] == "POST" {
			x1, _ := strconv.Atoi(m[2])
			y1, _ := strconv.Atoi(m[3])
			x2, _ := strconv.Atoi(m[4])
			y2, _ := strconv.Atoi(m[5])
			lx, ly = (x1+x2)/2, (y1+y2)/2
		}
	}
	tap(lx, ly, false)
	saveFrame("hover-portrait")
	before := countPath("/test/post")
	tap(lx, ly, true)
	time.Sleep(8 * time.Second)
	t.Logf("RESULT portrait click at (%d,%d): POST events before=%d after=%d", lx, ly, before, countPath("/test/post"))
	t.Log("probe done")
}
