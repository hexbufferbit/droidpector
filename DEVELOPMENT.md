# Development

## Layout

```
cmd/droidpector/      composition root: Windows host window + supervisor, --core, --headless
cmd/testserver/        deterministic HTTP/HTTPS/WS test server (standalone)
src/model              event/session types (no dependencies)
src/content            Content-Encoding decoding, body classification
src/query              filter language + Network Query Service
src/export             cURL (Generator interface), HAR 1.2
src/storage            SQLite schema/migrations, async writer, blob store, retention
src/network            user-mode stack, DHCP, DNS, gateway, TLS MITM, HTTP/WS capture, replay
src/network/guestsim   simulated guest NIC (tests, devserver)
src/android/{adb,apk,device}  ADB wire protocol, APK analysis, device operations
src/vm                 profiles, QEMU args, QMP, console, machine lifecycle, disks
src/display            RFB client and frame streamer
src/core               App (wiring), Sandbox orchestrator, APKs, Sessions, Recorder, Hub
src/ipc                HTTP/WebSocket API
src/platform           paths, config, logging, process groups, DPAPI
src/ui                 embedded web UI (src/ui/web: React + TS + Vite)
src/installer          NSIS script, runtime fetch script, runtime manifests
src/testserver         test server library
tools/                 apkbuild, apkinfo, devprobe, devserver, runtime/*, testapp
tests/integration      network pipeline integration tests (no VM)
tests/vm               real-VM integration tests (-tags vm)
tests/e2e              Windows installer smoke test
```

Dependency direction: `ipc → core → {vm, android, display, network, query,
storage, export} → model`; `platform` is used by all; nothing imports `ipc`
or `core` except the composition root. The UI talks only to `ipc`.

## Everyday loops

```bash
go test ./...                              # fast: everything except the real VM
go run ./tools/devserver -port 8765        # real core + capture, simulated guest; prints URL
cd src/ui/web && npm run dev               # UI with hot reload against the devserver
go run ./cmd/droidpector --headless --open  # real core; needs DROIDPECTOR_RUNTIME for a VM
```

Useful environment variables:

| variable | effect |
|---|---|
| `DROIDPECTOR_HOME` | data directory (default `%LOCALAPPDATA%\droidpector`) |
| `DROIDPECTOR_RUNTIME` | runtime directory (default `<install dir>\runtime`) |
| `NET_DEBUG=1` | debug logs in network integration tests |

Developer tools: `go run ./tools/apkinfo app.apk` (what the APK manager sees),
`go run ./tools/devprobe -addr host:port` (talk to any adbd with our ADB
client), `go run ./tools/apkbuild` (build/sign APKs without the SDK).

## Configuration

`%LOCALAPPDATA%\droidpector\config.json` (created with defaults, values
clamped, corrupt files preserved as `.corrupt`): memory/CPUs, accelerator,
boot timeout, auto-restart, quick-start snapshot, HTTPS inspection, LAN/QUIC
blocking, body limit, host mappings, extra upstream roots, retention, log
level (TRACE…FATAL).

## Logging

Structured JSON lines, rotated (20 MB × 5, gzip): `logs/app.log`,
`logs/vm.log` (QEMU command line, stderr, lifecycle), `logs/network.log`,
plus `logs/android-console.log` (guest serial console). The diagnostic bundle
(`GET /api/diagnostics/bundle`, "Save diagnostic bundle" in the UI) zips logs,
configuration and runtime statistics.

## Rules

SOLID/KISS/YAGNI, explicit ownership and contexts for all I/O, no global
mutable state or singletons (everything is constructed in `core.NewApp`),
errors wrapped with actionable messages (never "unknown error"), panics only
for programmer errors and always recovered at flow boundaries, deterministic
tests (no sleeps for synchronization, no Internet).
