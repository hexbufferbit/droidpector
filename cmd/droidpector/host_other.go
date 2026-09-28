//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"

	"github.com/droidpector/apkinspector/src/platform"
)

// runHost on development platforms supervises the core and opens the system
// browser (the product window is Windows-only).
func runHost() int {
	procs, err := platform.NewProcessGroup()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer procs.Close()
	sup := &supervisor{procs: procs, onURL: openURL, onFailed: func(msg string) { fmt.Fprintln(os.Stderr, msg) }}
	if err := sup.start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("droidpector UI:", sup.url())
	openURL(sup.url())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	sup.stop()
	return 0
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
