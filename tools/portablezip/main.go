// Command portablezip packs the portable droidpector folder into a zip whose
// single top-level directory is the application folder. Already-compressed
// files (the xz Android image, PNG/JPEG) are stored, everything else is
// deflated. ZIP64 is used automatically for large archives.
//
//	go run ./tools/portablezip -src build/portable/droidpector -out build/droidpector-portable-x64.zip
package main

import (
	"archive/zip"
	"compress/flate"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var stored = map[string]bool{".iso": true, ".png": true, ".jpg": true, ".zip": true, ".gz": true}

func main() {
	src := flag.String("src", "", "folder to pack (becomes the top-level folder)")
	out := flag.String("out", "", "output zip")
	flag.Parse()
	if *src == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := pack(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "portablezip:", err)
		os.Exit(1)
	}
}

func pack(src, out string) error {
	f, err := os.Create(out + ".tmp")
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestCompression) })
	root := filepath.Base(src)
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var files int
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h := &zip.FileHeader{Name: root + "/" + filepath.ToSlash(rel), Method: zip.Deflate, Modified: mtime}
		if stored[strings.ToLower(filepath.Ext(p))] {
			h.Method = zip.Store
		}
		h.SetMode(info.Mode())
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		files++
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out + ".tmp")
		return err
	}
	fmt.Printf("portablezip: %d files → %s\n", files, out)
	return os.Rename(out+".tmp", out)
}
