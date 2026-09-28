// Command apkinfo prints the analysis droidpector performs on an APK
// (package, versions, label, ABIs, runtime decision, validation issues).
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/droidpector/apkinspector/src/android/apk"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apkinfo <file.apk>")
		os.Exit(2)
	}
	info, err := apk.Inspect(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "apkinfo:", err)
		os.Exit(1)
	}
	out := map[string]any{"info": info, "runtime": info.RequiredRuntime(true), "runtimeWithoutTranslation": info.RequiredRuntime(false)}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
