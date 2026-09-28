//go:build windows

package main

import (
	"html"
	"os"
	"path/filepath"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/droidpector/apkinspector/src/platform"
)

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}

// singleInstance prevents two copies from fighting over the sandbox disk.
func singleInstance() (release func(), ok bool) {
	name, _ := windows.UTF16PtrFromString("Local\\droidpector-SingleInstance")
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return nil, false
	}
	return func() { windows.CloseHandle(h) }, true
}

func runHost() int {
	release, ok := singleInstance()
	if !ok {
		messageBox("droidpector", "droidpector is already running.")
		return 1
	}
	defer release()
	procs, err := platform.NewProcessGroup()
	if err != nil {
		messageBox("droidpector", "droidpector could not start: "+err.Error())
		return 1
	}
	defer procs.Close()

	paths, err := platform.ResolvePaths()
	if err != nil {
		messageBox("droidpector", err.Error())
		return 1
	}
	os.MkdirAll(paths.DataDir, 0o700)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(paths.DataDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: "droidpector", Width: 1440, Height: 900, Center: true, IconId: 1,
		},
	})
	if w == nil {
		messageBox("droidpector",
			"The Microsoft Edge WebView2 Runtime is missing.\n\nIt is installed with Windows 11 and current Windows 10 updates. Reinstall droidpector (the installer adds it) or install it from microsoft.com/edge/webview2.")
		return 1
	}
	defer w.Destroy()
	w.SetSize(1100, 700, webview2.HintMin)
	w.SetHtml(loadingPage("Starting droidpector..."))

	sup := &supervisor{procs: procs}
	sup.onURL = func(url string) { w.Dispatch(func() { w.Navigate(url) }) }
	sup.onFailed = func(msg string) { w.Dispatch(func() { w.SetHtml(loadingPage(msg)) }) }
	go func() {
		if err := sup.start(); err != nil {
			sup.onFailed("droidpector could not start: " + err.Error())
			return
		}
		sup.onURL(sup.url())
	}()
	w.Run()
	sup.stop()
	return 0
}

func loadingPage(msg string) string {
	return `<!doctype html><html><body style="margin:0;display:flex;align-items:center;justify-content:center;height:100vh;` +
		`font-family:Segoe UI,sans-serif;background:#1e1f22;color:#dcdfe4"><div style="text-align:center;max-width:560px">` +
		`<div style="font-size:20px;margin-bottom:12px">droidpector</div><div>` + html.EscapeString(msg) + `</div></div></body></html>`
}

func openURL(url string) {
	u, _ := windows.UTF16PtrFromString(url)
	o, _ := windows.UTF16PtrFromString("open")
	windows.ShellExecute(0, o, u, nil, nil, windows.SW_SHOWNORMAL)
}
