// Command qemuprune copies the minimal QEMU-for-Windows runtime out of an
// extracted QEMU installer: the requested executables, the transitive closure
// of the DLLs they import (resolved from their PE import tables), the firmware
// files the Android machine types need, and the license texts.
//
//	go run ./tools/runtime/qemuprune -src extracted/ -dst build/runtime/qemu \
//	    -exe qemu-system-x86_64.exe -exe qemu-system-aarch64.exe -exe qemu-img.exe
package main

import (
	"debug/pe"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// firmware needed by q35 (x86_64) and virt (aarch64) with our devices.
var firmware = []string{
	"bios-256k.bin", "kvmvapic.bin", "linuxboot_dma.bin", "linuxboot.bin", "vgabios-stdvga.bin", "vgabios-ramfb.bin",
	"efi-virtio.rom", "efi-e1000e.rom", "multiboot_dma.bin", "pvh.bin",
	"edk2-aarch64-code.fd", "edk2-arm-vars.fd", "edk2-licenses.txt",
	"keymaps/en-us", "keymaps/common", "keymaps/modifiers",
}

func main() {
	src := flag.String("src", "", "extracted QEMU installer directory")
	dst := flag.String("dst", "", "output directory")
	var exes multi
	flag.Var(&exes, "exe", "executable to include (repeatable)")
	flag.Parse()
	if *src == "" || *dst == "" || len(exes) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*src, *dst, exes); err != nil {
		fmt.Fprintln(os.Stderr, "qemuprune:", err)
		os.Exit(1)
	}
}

func run(src, dst string, exes []string) error {
	// Index DLLs shipped with QEMU (case-insensitive, as Windows resolves them).
	shipped := map[string]string{}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.EqualFold(filepath.Ext(e.Name()), ".dll") {
			shipped[strings.ToLower(e.Name())] = e.Name()
		}
	}
	need := map[string]bool{}
	var visit func(file string) error
	visit = func(file string) error {
		f, err := pe.Open(filepath.Join(src, file))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		// debug/pe's ImportedLibraries is unimplemented; derive the DLL set
		// from ImportedSymbols ("symbol:library.dll").
		syms, err := f.ImportedSymbols()
		f.Close()
		if err != nil {
			return fmt.Errorf("%s imports: %w", file, err)
		}
		libSet := map[string]bool{}
		for _, s := range syms {
			if i := strings.LastIndex(s, ":"); i >= 0 {
				libSet[s[i+1:]] = true
			}
		}
		for l := range libSet {
			name, ok := shipped[strings.ToLower(l)]
			if !ok || need[name] {
				continue // system DLL (kernel32, …) or already included
			}
			need[name] = true
			if err := visit(name); err != nil {
				return err
			}
		}
		return nil
	}
	files := map[string]bool{}
	for _, exe := range exes {
		files[exe] = true
		if err := visit(exe); err != nil {
			return err
		}
	}
	for d := range need {
		files[d] = true
	}
	for _, fw := range firmware {
		if _, err := os.Stat(filepath.Join(src, "share", fw)); err == nil {
			files[filepath.Join("share", fw)] = true
		}
	}
	for _, lic := range []string{"COPYING", "COPYING.LIB", "LICENSE", "README.rst"} {
		if _, err := os.Stat(filepath.Join(src, lic)); err == nil {
			files[lic] = true
		}
	}
	var list []string
	for f := range files {
		list = append(list, f)
	}
	sort.Strings(list)
	var total int64
	for _, f := range list {
		n, err := copyFile(filepath.Join(src, f), filepath.Join(dst, f))
		if err != nil {
			return err
		}
		total += n
	}
	fmt.Printf("qemuprune: %d files, %.1f MiB (%d DLLs)\n", len(list), float64(total)/(1<<20), len(need))
	return nil
}

func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}
