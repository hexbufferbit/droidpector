// Command droidpector-agent runs inside the Android guest (as root). It
// creates a virtual touchscreen with /dev/uinput and injects the touch
// events it receives from the core over TCP, so pointer input reaches apps
// as real touches — orientation-aware, with genuine touch semantics — instead
// of going through the emulated USB mouse, whose absolute positioning is
// broken in rotated displays on Android-x86.
//
// Wire protocol (client → agent), 5-byte frames: [op u8][x u16 BE][y u16 BE]
// with coordinates in natural (panel) pixels; op 0 = down, 1 = move, 2 = up,
// 3 = scroll (x,y = position; a following frame [4][dx i16][dy i16] gives the
// direction). The agent replies "OK <w>x<h>\n" once after the connection.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/droidpector/apkinspector/src/android/agent"
	"github.com/droidpector/apkinspector/src/android/agent/uinput"
)

func main() {
	listen := flag.String("listen", ":5560", "TCP address to accept the core on")
	width := flag.Int("width", 720, "panel width (natural orientation)")
	height := flag.Int("height", 1280, "panel height (natural orientation)")
	flag.Parse()
	ts, err := uinput.NewTouchscreen("droidpector touch", *width, *height)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidpector-agent:", err)
		os.Exit(1)
	}
	defer ts.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidpector-agent:", err)
		os.Exit(1)
	}
	fmt.Printf("droidpector-agent: touchscreen %dx%d, listening on %s\n", *width, *height, *listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			agent.Serve(c, c, ts, *width, *height)
		}(c)
	}
}
