// Command apkinspector is droidpector: the composition root.
//
// Modes:
//
//	(default)   Windows: native window (WebView2) that supervises a core child
//	            process. Other OSes (development): core + system browser.
//	--core      Run the application core and its local API; print the UI URL
//	            as a JSON line on stdout; exit when stdin closes (parent died).
//	--headless  Like --core but for manual/CI use: logs to stderr, no stdin watch.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/droidpector/apkinspector/src/core"
	"github.com/droidpector/apkinspector/src/ipc"
	"github.com/droidpector/apkinspector/src/platform"
	"github.com/droidpector/apkinspector/src/ui"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0-dev"

// ready is the handshake line the core prints for the host.
type ready struct {
	URL   string `json:"url"`
	Base  string `json:"base"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func main() {
	coreMode := flag.Bool("core", false, "run the application core (used by the window host)")
	headless := flag.Bool("headless", false, "run the core only and print the UI URL")
	port := flag.Int("port", 0, "local API port (0 = random)")
	openBrowser := flag.Bool("open", false, "open the UI in the system browser (headless/dev)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	switch {
	case *coreMode || *headless:
		os.Exit(runCore(*port, *headless, *openBrowser))
	default:
		os.Exit(runHost())
	}
}

// runCore starts the core and blocks until shutdown.
func runCore(port int, headless, open bool) int {
	paths, err := platform.ResolvePaths()
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidpector:", err)
		return 2
	}
	if err := paths.Ensure(); err != nil {
		fmt.Fprintln(os.Stderr, "droidpector:", err)
		return 2
	}
	cfg, cfgErr := platform.LoadConfig(paths.ConfigFile)
	level, _ := platform.ParseLevel(cfg.LogLevel)
	logs, err := platform.OpenLoggers(paths.LogDir, level, headless)
	if err != nil {
		fmt.Fprintln(os.Stderr, "droidpector: opening logs:", err)
		return 2
	}
	defer logs.Close()
	if cfgErr != nil {
		logs.App.Warn("configuration problem", "err", cfgErr)
	}
	logs.App.Info("droidpector core starting", "version", version, "data", paths.DataDir, "runtime", paths.RuntimeDir)
	defer func() {
		if r := recover(); r != nil {
			platform.Fatal(logs.App, "core crashed", "panic", fmt.Sprint(r))
			panic(r)
		}
	}()

	app, err := core.NewApp(version, paths, cfg, logs)
	if err != nil {
		platform.Fatal(logs.App, "core initialization failed", "err", err)
		fmt.Fprintln(os.Stderr, "droidpector:", err)
		return 3
	}
	defer app.Close()
	srv, err := ipc.NewServer(app, ui.Assets(), port, logs.App)
	if err != nil {
		platform.Fatal(logs.App, "API server failed", "err", err)
		return 3
	}
	go func() {
		if err := srv.Serve(); err != nil {
			logs.App.Error("API server stopped", "err", err)
		}
	}()
	line, _ := json.Marshal(ready{URL: srv.URL(), Base: srv.BaseURL(), Token: srv.Token(), PID: os.Getpid()})
	fmt.Println(string(line))
	logs.App.Info("core ready", "url", srv.BaseURL())
	if open {
		openURL(srv.URL())
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	parentGone := make(chan struct{})
	if !headless {
		// The host owns our stdin: EOF means the window process exited.
		go func() {
			io.Copy(io.Discard, bufio.NewReader(os.Stdin))
			close(parentGone)
		}()
	}
	select {
	case <-stop:
		logs.App.Info("shutdown requested")
	case <-parentGone:
		logs.App.Info("host process ended; shutting down")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	return 0
}
