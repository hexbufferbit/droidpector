# Build

## Toolchain

| Tool | Version | Used for |
|---|---|---|
| Go | 1.27+ | core, host, tools, tests |
| Node.js / npm | 22+ | web UI (React + TypeScript + Vite) |
| NSIS | 3.x | installer (`makensis`) |
| 7-Zip, qemu-img, e2fsprogs, erofs-utils, squashfs-tools, xorriso, curl | any recent | `make runtime` (Linux/macOS) |
| JDK | 11+ | TestApp (`make testapp`) |

No CGO is required: the Windows executable is cross-compiled from any OS
(`GOOS=windows GOARCH=amd64 CGO_ENABLED=0`).

## Targets

```bash
make ui             # src/ui/web → dist (embedded via go:embed)
make agent          # in-guest touch agent (static linux/amd64, embedded via go:embed)
make windows        # Release: build/droidpector.exe  (-trimpath -s -w, GUI subsystem)
make windows-debug  # Debug:   build/debug/droidpector.exe (no optimizations, console output)
make runtime        # build/runtime: pinned QEMU for Windows + Android runtime + licenses
make installer      # build/droidpector-Setup-x64.exe (single self-contained installer)
make testapp        # build/testapp/TestApp.apk
```

The production pipeline is `windows-x64` in `.github/workflows/ci.yml`; its
package job only runs after build, unit, integration, VM integration and
static-analysis jobs pass, and it uploads:

```
build/droidpector.exe
build/droidpector-Setup-x64.exe
build/debug/droidpector.exe
```

## Versioning

`VERSION` defaults to `git describe`; it is embedded with
`-X main.version=…` (`droidpector.exe --version`) and in the installer's
version resource.

## Runtime bundle

`tools/runtime/prepare.sh` downloads (SHA-256 pinned) the QEMU Windows build
and the BlissOS 16 image, extracts kernel/initrd, repacks the system image
losslessly into an xz squashfs ISO (verified byte-identical, reproducible),
creates the ext4 data template, prunes QEMU to the executables we use plus the DLLs they import
(`tools/runtime/qemuprune` walks PE import tables) and writes license texts
and the source-code offer. Result layout:

```
build/runtime/
  qemu/                       qemu-system-x86_64.exe, qemu-system-aarch64.exe, qemu-img.exe, *.dll, share/
  android/x86_64/             runtime.json, kernel, initrd.img, data-template.qcow2, system.iso
  licenses/                   NOTICE.txt, SOURCES.txt, QEMU-COPYING*.txt
```

## Notes for macOS development machines

- Homebrew's `makensis` 3.13 aborts with `std::bad_alloc` on macOS 27 when
  ASLR is enabled. Build the installer on Linux/Windows (CI), or run it under
  `lldb --batch -o "run <args>" -- makensis` locally.
- The x86_64 Android guest runs under TCG on Apple Silicon (≈4 minutes to a
  ready sandbox); use a Linux/KVM or Windows/WHPX machine for fast iterations.
