// Command devprobe connects to a running guest's adbd over TCP with the
// project's own ADB client and prints basic device facts (developer tool).
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/droidpector/apkinspector/src/android/adb"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:15555", "adbd TCP address")
	cmd := flag.String("cmd", "getprop ro.build.version.release; getprop sys.boot_completed; id; getprop ro.product.cpu.abilist", "shell command")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	key, _ := adb.GenerateKey(rand.Reader)
	c, err := adb.Connect(ctx, adb.Config{Key: key, Dial: func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", *addr)
	}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer c.Close()
	info := c.Info()
	fmt.Println("state:", info.State, "features:", strings.Join(info.FeatureList(), ","))
	out, errOut, code, err := c.Shell(ctx, *cmd)
	fmt.Printf("exit=%d err=%v\n%s%s", code, err, out, errOut)
}
