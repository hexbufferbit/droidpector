# droidpector

A Windows x64 desktop application that runs an Android APK inside an isolated
Android virtual machine and shows **only that sandbox's** network traffic in a
DevTools-style Network Inspector.

```
Install EXE → Open App → Drop APK → Run APK → Use Android App → Inspect Network → Click Request → Copy as cURL
```

Users never deal with QEMU, ADB, virtual networks, proxies or certificates:
the application manages all of them.

## Features

- **Android sandbox** — Android 13 (x86_64, with ARM-to-x86 translation for
  64-bit ARM apps) under QEMU with Windows Hypervisor Platform acceleration
  (automatic software-emulation fallback), start/stop/restart/reset, snapshots,
  quick-start snapshot, crash detection and automatic recovery.
- **APK manager** — drag & drop or file picker, validation (signature, ABIs,
  SDK levels), automatic runtime selection by CPU architecture, install,
  launch, stop, clear data, uninstall, reinstall, meaningful install errors.
- **Embedded Android display** — mouse, touch-like drag, keyboard, scroll,
  paste, scaled to the window.
- **Network isolation by construction** — the VM's only NIC is a user-mode
  TCP/IP stack inside the app. Windows traffic never enters the capture
  pipeline; no system proxy, driver or TAP adapter is used.
- **Network Inspector** — HTTP/1.1, HTTP/2, HTTPS (per-boot in-memory CA
  installed into the sandbox), WebSocket frames, DNS, raw TCP/TLS metadata;
  certificate-pinning detection with automatic passthrough; virtualized list
  for 100k+ requests; filter language (`host:`, `method:`, `status:4xx`,
  `host contains "x"`, …) and quick filters; details (Overview, Headers,
  Query, Request, Response with JSON tree/XML/HTML/text/image/hex viewers,
  Timing, Connection).
- **Export** — Copy as cURL, Copy headers/response, Save request (HAR),
  Export session as HAR 1.2, Replay (as a new event).
- **Sessions** — every sandbox run is a session (start/stop/clear/save/export),
  stored in SQLite with bodies in a compressed content-addressed blob store.

## Requirements

End users: Windows 10/11 x64, 8 GB RAM (16 GB recommended), ~6 GB disk,
hardware virtualization enabled (for full speed). The installer is a single
self-contained file (≈1.7 GB) and needs no Internet connection.

Developers: Go 1.27+, Node.js 22+, and for packaging: NSIS 3, 7-Zip,
qemu-img, e2fsprogs, curl; JDK 11+ for the TestApp. See [BUILD.md](BUILD.md).

## Build

```bash
make ui            # web UI → embedded into the exe
make windows       # build/droidpector.exe (Release, windows/amd64)
make runtime       # build/runtime (pinned QEMU + Android runtime)
make portable      # build/droidpector-portable-x64.zip (extract & run)
make installer     # build/droidpector-Setup-x64.exe (optional)
```

## Run

- Windows: extract `droidpector-portable-x64.zip` anywhere writable and run `droidpector.exe` (everything stays in that folder).
- Development (any OS): `go run ./cmd/droidpector --headless --open` starts
  the core and opens the UI in a browser (set `DROIDPECTOR_RUNTIME` to a
  runtime folder to boot Android). `go run ./tools/devserver` runs the real
  capture pipeline with a simulated guest (no VM) for UI work.

## Test

```bash
make test          # unit + integration (simulated guest, real gateway/TLS/storage)
make lint          # gofmt, go vet (host + windows), staticcheck, UI typecheck/lint
make e2e           # Playwright UI tests against the real backend
make test-vm       # real Android VM: boot, ADB, install, launch, capture, crash recovery
```

See [TESTING.md](TESTING.md).

## Package

`make installer` produces the NSIS installer; CI (`.github/workflows/ci.yml`)
runs Build → Unit → Integration → Static analysis → Package → E2E and a
clean-Windows install/boot/uninstall smoke test. See [PACKAGING.md](PACKAGING.md).

## Architecture overview

```
droidpector.exe (window, WebView2) ──supervises──► core (same exe, --core)
                                                        ├─ ipc      HTTP/WS API (127.0.0.1 + token)
UI (React) ◄──── HTTP/WS ─────────────────────────────  ├─ core     orchestration, sessions, APK workflow
                                                        ├─ vm       QEMU adapter, QMP, console, snapshots
                                                        ├─ android  ADB protocol, APK parser, device ops
                                                        ├─ display  RFB client → frame stream
                                                        ├─ network  user-mode stack, DHCP/DNS, HTTP(S)/WS capture
                                                        ├─ query    filter engine, paging
                                                        └─ storage  SQLite + blob store
qemu-system-x86_64 ◄── only NIC = socket link into the core's network stack
```

Details: [ARCHITECTURE.md](ARCHITECTURE.md) · [NETWORK.md](NETWORK.md) ·
[VM.md](VM.md) · [SECURITY.md](SECURITY.md) · [DEVELOPMENT.md](DEVELOPMENT.md)

## Legal

Use droidpector only on applications you are authorized to analyse.
Third-party components and their licenses: `runtime/licenses` (NOTICE.txt,
SOURCES.txt).
