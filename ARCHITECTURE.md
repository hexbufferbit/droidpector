# Architecture — droidpector

droidpector is a Windows x64 desktop application that runs an APK inside an
isolated Android virtual machine and shows **only that VM's** network traffic in a
DevTools-style inspector.

This document records the architecture and every significant technical decision
with its rationale (ADR style). Detailed subsystem docs: [VM.md](VM.md),
[NETWORK.md](NETWORK.md), [SECURITY.md](SECURITY.md), [PACKAGING.md](PACKAGING.md),
[TESTING.md](TESTING.md).

---

## 1. System overview

```
┌──────────────────────── droidpector.exe (host process) ────────────────────────┐
│  Native window (WebView2)   ── loads ──►  UI (TypeScript/React, embedded assets)   │
│  Supervises the core process, restarts it on crash, shows fatal-error page         │
└───────────────┬────────────────────────────────────────────────────────────────────┘
                │ spawns (Job Object: kill-on-close)            ▲ HTTP + WebSocket
                ▼                                               │ 127.0.0.1:<random>,
┌──────────────────────── core process (same exe, --core) ──────┴─ bearer/cookie token ┐
│  ipc/        HTTP API, event stream, display stream, auth, static UI assets          │
│  core/       Application/control layer: Sandbox orchestrator, APK workflow, Sessions │
│  ├─ vm/        VM service: runtime profiles, QEMU adapter, QMP, supervisor, snapshots│
│  ├─ android/   ADB wire protocol client, device ops, APK parser, CA installer         │
│  ├─ display/   RFB (VNC) client → framebuffer → dirty-rect stream to UI              │
│  ├─ network/   User-mode network stack (gVisor netstack) = the VM's only NIC         │
│  │             DHCP, DNS, TCP/UDP flows, HTTP(S)/WS capture, TLS MITM, replay        │
│  ├─ query/     Network Query Service (filter engine, paging, live counts)            │
│  ├─ storage/   SQLite (metadata) + content-addressed blob store (bodies)             │
│  └─ export/    HAR, cURL (extensible code generators)                                │
└───────────────┬──────────────────────────────────────────────────────────────────────┘
                │ spawns (Job Object)          ▲ Ethernet frames over a private socket
                ▼                              │ (QEMU "socket" netdev, length-prefixed)
          qemu-system-x86_64.exe  ─────────────┘   (or qemu-system-aarch64.exe)
          Android 13 guest (BlissOS 16 x86_64)  — no other network interface
```

### Why traffic isolation is inherent (not filtered)

The guest's **only** NIC is a QEMU `socket` netdev connected to the core process.
Ethernet frames from the guest are fed into a private user-mode TCP/IP stack
(gVisor netstack) inside the core. Every connection the guest makes terminates in
that stack, and the core re-originates it to the Internet. Windows host traffic
never enters this pipeline — there is no system proxy, no WinPcap/Npcap driver,
no TAP adapter, no host routing change. The capture pipeline *physically* only
sees frames produced by the VM.

## 2. Layering and dependency direction

```
ui (web) ──HTTP/WS──► ipc ──► core ──► vm ──► qemu adapter/qmp
                              │   ├──► android (adb, apk)
                              │   ├──► display (rfb)
                              │   └──► network ──► model
                              └──► query ──► storage ──► model
                                   export ──► model
platform (paths, logging, process/job objects, config) ◄── used by all
```

Rules:
* `model` (event/session types) depends on nothing. Capture (`network`) and UI
  never import each other; they meet only through `model` + `storage`/`query`.
* The UI never controls QEMU: `UI → ipc → core.Sandbox → vm.Service → qemu.Adapter`.
* Network queries: `UI → ipc → query.Service → storage.Store`.
* Interfaces are declared by the consumer (dependency inversion), e.g. `core`
  declares `VM`, `Device`, `EventSink` interfaces that `vm`, `android`, `storage`
  satisfy; tests substitute real lightweight implementations or fakes **only in tests**.
* No singletons / global mutable state: all services are constructed in
  `cmd/droidpector` (composition root) and passed explicitly.

### Source layout

The requested `/src/{core,vm,android,network,storage,ipc,ui,installer}` layout is kept.
Go unit tests live next to the code (Go convention); cross-package suites live in `/tests`.

```
cmd/droidpector/      composition root: host window (Windows) + --core mode
cmd/testserver/        deterministic HTTP/HTTPS/WS test server
src/model/             Event, Session, Timing, TLS info … (pure types)
src/platform/          paths (Known Folders), config, slog logging, process/job objects
src/core/              orchestrator, session manager, APK workflow, state machine
src/vm/                runtime profiles, QEMU adapter, QMP client, supervisor, snapshots
src/android/           adb protocol client, device ops, apk (zip/AXML/arsc) parser, CA install
src/display/           RFB client + frame encoder
src/network/           netstack link, dhcp, dns, gateway, httpcap, tlsmitm, ws, replay
src/query/             filter language (lexer/parser/eval), query service
src/storage/           sqlite schema/migrations, async writer, blob store, retention
src/export/            har, curl (Generator interface for future Python/JS/Go)
src/ipc/               HTTP API, WebSocket hubs, auth
src/ui/web/            React + TypeScript frontend (Vite), embedded into the exe
src/installer/         NSIS script, WebView2 bootstrapper handling
tools/                 runtime fetch/prepare scripts, TestApp builder
tests/{integration,e2e}  Go test suites (build tags) + Playwright UI tests
```

---

## 3. Architecture decisions (ADR)

### ADR-001 — Core in Go instead of C++ (deviation from the suggested C++/Qt)

**Decision:** Core and host are written in Go; UI is a WebView2-hosted TypeScript app.

**Rationale (ranked by the stated priorities):**
1. *Production reliability*: the most security-sensitive part — parsing hostile
   network traffic from arbitrary APKs, TLS MITM, HTTP/1.1, HTTP/2, WebSocket —
   is done in a memory-safe language with a battle-tested standard library
   (`net/http`, `crypto/tls`, `crypto/x509`). In C++ we would need
   OpenSSL + nghttp2 + a hand-rolled HTTP proxy: much larger attack surface.
2. *Windows x64*: Go produces a static `windows/amd64` binary with no runtime DLLs.
3. *The key isolation component exists in Go*: gVisor netstack is a production
   user-mode TCP/IP stack already used on Windows at scale (Podman/`gvisor-tap-vsock`).
4. *Easy packaging*: one `.exe` (UI assets embedded via `go:embed`), cross-compiled
   deterministically; no Qt deployment (`windeployqt`, plugin dirs, LGPL relinking).
5. *Automated testing*: `go test -race`, fuzzing for parsers, and the core runs
   headless on Linux CI (with KVM) for full VM integration/E2E runs.

**Trade-off:** Qt/QML gives a native widget feel; WebView2 gives a modern DevTools-like
UI at lower cost (DevTools itself is a web UI). WebView2 is present on Windows 11 and
updated Windows 10; the installer bootstraps it otherwise.

### ADR-002 — Two processes: host (UI) and core

The host process owns the window and supervises the core (`droidpector.exe --core`,
same binary). Consequences required by the spec:
* **Network engine crash ≠ UI crash**: every flow handler runs with panic recovery;
  a core-process crash is detected by the host, which shows a status and restarts core.
* **UI crash ≠ core corruption**: SQLite in WAL mode with transactional batched writes;
  WebView2 renderer crashes are reloaded by the host; if the host dies, the Job Object
  terminates core and QEMU (no orphaned VMs), and the DB remains consistent.
* The UI only talks HTTP/WS, so it can be tested in any browser (Playwright).

### ADR-003 — Virtualization: QEMU (+WHPX) managed by the core

QEMU is bundled (unmodified upstream Windows build, GPL-2.0, separate process →
no linking obligations beyond source offer). Acceleration: `-accel whpx` when the
Windows Hypervisor Platform is available, automatic fallback to TCG with a clear
UI warning. Control via QMP (JSON over a private socket). See [VM.md](VM.md).

### ADR-004 — Android runtime: hybrid ABI strategy (user decision 2026-09-28)

Requirement: 64-bit APKs must install without problems.
* **Default runtime `x86_64`**: BlissOS 16 FOSS (Android 13, API 33, x86_64 with
  x86 32-bit compat), WHPX-accelerated. Replaces Android-x86 9.0 (API 28 would reject
  modern apps with `INSTALL_FAILED_OLDER_SDK`).
* **ABI-aware placement**: the APK parser reads `lib/<abi>/` entries before install.
  No native code, `x86_64`, or `x86` → x86_64 runtime.
* **`arm64-v8a`/`armeabi-v7a`-only APKs**:
  1. if the user enabled the optional *ARM compatibility* component (a native-bridge
     translation layer — proprietary, therefore never bundled; user-initiated), the
     x86_64 runtime is used;
  2. otherwise the **`arm64` runtime profile** is used: an ARM64 Android guest under
     `qemu-system-aarch64` (TCG). Fully open and correct, but slower — the UI says so.
* The VM service is built around **runtime profiles** (`x86_64`, `arm64`) so either
  guest is driven by the same adapter, ADB, display and network code.

### ADR-005 — Network capture = user-mode stack + transparent gateway

See [NETWORK.md](NETWORK.md). Summary: gVisor netstack in promiscuous/forwarding mode
accepts every guest TCP/UDP flow; a protocol sniffer routes the flow to:
HTTP/1.x capture, TLS (MITM with per-boot CA → HTTP/1.1/HTTP/2 capture, or
passthrough with SNI metadata when pinned/declined), WebSocket frame capture, or raw
TCP accounting. DNS (UDP 53) is answered by the gateway and logged. UDP/443 (QUIC) is
rejected by default so apps fall back to inspectable TCP.

### ADR-006 — HTTPS inspection with a per-boot, in-memory CA

A fresh CA key pair is generated in memory each time the guest becomes available and
installed into the guest's **system** trust store through a tmpfs overlay (never written
to the guest disk image, gone on reboot). The private key never touches the host disk.
Pinning failures are detected and the host is automatically switched to passthrough
("HTTPS encrypted traffic detected. Payload inspection unavailable for this connection.").

### ADR-007 — ADB: own wire-protocol client over the private network

The core speaks the ADB transport protocol (CNXN/AUTH/OPEN/WRTE/OKAY/CLSE, sync, shell v2)
directly to `adbd` at the guest's private IP **through the user-mode stack**. No
`adb.exe`, no ADB server, no host TCP port is opened; ADB traffic is excluded from capture.

### ADR-008 — Display: VNC (RFB) on loopback, decoded in core

QEMU exposes VNC on `127.0.0.1:<random>` with a random per-boot password set via QMP.
Only the core connects; it decodes RFB and streams dirty rectangles to the UI over the
authenticated WebSocket; pointer/keyboard events flow back (absolute `usb-tablet`).
Paste is implemented via ADB text input.

### ADR-009 — Storage: SQLite (pure Go) + content-addressed blob store

`modernc.org/sqlite` (no CGO → clean cross-compilation). Tables: `sessions`, `events`
(summary columns indexed for filtering), `event_details` (headers/timing/TLS JSON),
`meta`. Bodies go to `blobs/aa/<sha256>` (zstd-compressed when beneficial), referenced by
hash — never stored in the main table. Async writer with bounded queue, batching and
backpressure; per-body size limits; retention policy. See [NETWORK.md](NETWORK.md#storage).

### ADR-010 — Query/filter in core, virtualization in UI

The filter language is parsed/evaluated in the core (`query`), over an in-memory index
of summaries for the open session; the UI requests only the visible window
(`offset/limit`) and receives coalesced "count changed" notifications. The UI never
holds all events → scales to 100k+ requests.

### ADR-011 — Local API security

The core listens only on `127.0.0.1` on a random port; every request needs a 256-bit
random token (cookie set once from the host-provided URL, or bearer header). `Host` and
`Origin` headers are validated (DNS-rebinding defence). See [SECURITY.md](SECURITY.md).

### ADR-012 — Single self-contained installer (Android image repacked losslessly)

BlissOS ships its system image as a 2.2 GB EROFS (lz4) file, above the ~2 GB
payload limit of NSIS (and of most single-file installer formats). The build
extracts the ext4 `system.img` and re-compresses the *same bytes* into an xz
squashfs (1.64 GB) inside a small ISO that the BlissOS initrd discovers
(`ROOT=/dev/sr0`, `SRC=`). `prepare.sh` proves the repack is lossless
(`system.img` inside the squashfs is byte-identical to the original) and the
build is reproducible (`SOURCE_DATE_EPOCH`). The installer therefore embeds
everything (≈1.67 GiB, ~340 MiB headroom) and installs fully offline; the
image is stored uncompressed by NSIS (already xz). Trade-off: xz decompression
costs some guest CPU on cold file reads (mitigated by the quick-start
snapshot). Redistribution of the image — including BlissOS's proprietary
`libndk_translation` — must be cleared before public release (PACKAGING.md).

### ADR-013 — Device bootstrap: serial console + `adb root`

Before ADB is reachable the core drives Android's root serial console
(`androidboot.enable_console=1`) to ensure adbd listens on TCP 5555; the
console waits until Android's (not the initrd's) shell answers. ADB then
connects through the sandbox network and asks adbd to restart as root (the
`root:` service on userdebug images), which the system-CA overlay and clock
sync require. Without root the sandbox still works, with HTTPS payload
inspection reported as unavailable.

### ADR-014 — App-only traffic enforced inside the guest

Requirement: every connection of the sandbox OS other than the app under
test must be blocked. The gateway learns socket ownership asynchronously
(polling `/proc/net/tcp`), which is fine for labeling but not for
enforcement. The only place where the sender's UID is known synchronously is
the guest kernel, so the firewall is an iptables `owner` chain programmed
over ADB (root) — exactly how Android's own per-app restrictions work.
Consequences: blocked traffic never reaches the gateway (no noise, no leak);
DNS cannot be attributed per app and stays open; the gateway's egress
policy remains as defence in depth.

### ADR-015 — Phone-shaped display via EDID, rotation via Android

The guest display advertises the phone resolution (720×1280) through the
emulated VGA's EDID; the bochs-drm driver adopts it, so Android boots with a
portrait framebuffer and `wm density 320` gives phone scaling. Rotation uses
Android's `user_rotation` (apps really get a landscape configuration) and is
rendered into the same framebuffer; the UI counter-rotates the canvas and
inverse-maps pointer input. This avoids per-frame rotation in the core and
keeps a single, tested display path. Pointer input: QEMU's absolute USB tablet is handled by Android-x86 as a
mouse whose Y axis is double-scaled in rotated displays (measured on the real
VM: the cursor is pinned to the top strip in landscape, with either input
configuration), so it cannot drive apps after rotation. Clicks are therefore
delivered by a small **in-guest touch agent** (`cmd/droidpector-agent`, a
static Go binary pushed over ADB) that creates a virtual touchscreen with
`/dev/uinput` and receives finger events from the core through the sandbox
network. Android treats it like a phone's panel (orientation-aware, real
touch semantics: long-press, scroll gestures); the UI sends the framebuffer
pixel under the cursor. The emulated mouse remains the fallback for hover
and for guests without the agent.

---

## 4. Dependencies

| Component | Use | License | Windows x64 | Redistribution |
|---|---|---|---|---|
| Go toolchain + stdlib, `golang.org/x/{net,sys,crypto}` | core | BSD-3 | yes | yes |
| gVisor netstack (`gvisor.dev/gvisor`, go branch) | user-mode TCP/IP | Apache-2.0 | yes (used by Podman on Windows) | yes |
| `modernc.org/sqlite` | storage | BSD-3 | yes, no CGO | yes |
| `klauspost/compress` (zstd), `andybalholm/brotli` | body codecs | BSD-3 / MIT | yes | yes |
| `jchv/go-webview2` | native window | MIT | yes, no CGO | yes |
| `natefinch/lumberjack` | log rotation | MIT | yes | yes |
| React, Vite, TypeScript | UI (build-time) | MIT / Apache-2.0 | n/a | bundled JS is MIT |
| Microsoft Edge WebView2 Runtime (Evergreen bootstrapper) | UI runtime | MS redistributable | yes | yes (bootstrapper) |
| QEMU (Stefan Weil Windows builds) | VM | GPL-2.0 | yes | yes, unmodified + source offer |
| BlissOS 16 FOSS x86_64 | Android runtime | Apache-2.0 / GPL-2.0 (kernel) | guest | yes, unmodified + source offer |
| NSIS | installer (build-time) | zlib | yes | n/a |
| Playwright | UI E2E tests (dev only) | Apache-2.0 | yes | not shipped |

Not bundled: Google apps/services, libhoudini/libndk_translation (proprietary).

---

## 5. Risk analysis

| Risk | Impact | Mitigation |
|---|---|---|
| WHPX unavailable / disabled | Android very slow | Detect, fall back to TCG, explain how to enable "Windows Hypervisor Platform"; optional installer step |
| Hyper-V/VBS conflicts | start failure | QEMU stderr classified into actionable error messages |
| ARM-only APKs | install failure | ADR-004 hybrid strategy; pre-install ABI check |
| arm64 TCG performance | poor UX for ARM apps | clear expectation in UI; boot snapshot to skip cold boot |
| Certificate pinning | no payload | auto passthrough + metadata + explicit UI message |
| Apps ignoring system CA (custom trust) | handshake failure | same as pinning |
| QUIC/HTTP3 | uninspectable UDP | block UDP/443 (configurable) → TCP fallback |
| Guest clock after snapshot restore | TLS errors | resync time via ADB after every restore |
| Large installer (~2.5 GB) | download size | LZMA; runtime images already compressed; documented |
| Hostile traffic / parser bugs | core crash | memory-safe parsing, per-flow panic recovery, fuzz tests, body limits |
| Guest reaching host services | sandbox escape vector | gateway denies loopback, link-local, metadata IPs by default |
| Cannot validate on real Windows in the dev environment | undetected Windows issues | Windows CI jobs build, install silently and run smoke/E2E |
| 2 GB installer payload limit | cannot embed the Android image | ADR-012 lossless xz repack (1.64 GB) → single installer |

---

## 6. Implementation plan (milestones)

| # | Milestone | Verification |
|---|---|---|
| M0 | Repo skeleton, build system, docs, CI skeleton | build + lint green |
| M1 | Domain model, HTTP/URL/header utils, filter engine, cURL, HAR, storage, sessions | unit tests |
| M2 | Network engine: netstack link, DHCP, DNS, gateway, HTTP/TLS/WS capture, replay; test server | integration tests with a simulated guest (second netstack speaking the QEMU socket protocol) |
| M3 | VM service (QEMU/QMP/supervisor/snapshots, runtime profiles), ADB client, APK parser, device ops, CA install | unit + integration against real QEMU guest |
| M4 | IPC API, display streaming, core orchestrator, UI | API tests, UI unit tests, Playwright |
| M5 | Windows host exe, runtime fetch, NSIS installer, CI pipeline `windows-x64` | CI artifacts |
| M6 | TestApp APK, E2E suites (API-level and UI-level), crash-recovery tests | E2E green |
