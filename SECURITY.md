# Security

The application executes untrusted APKs and parses untrusted network
traffic. Controls, by threat:

## Guest → host

| Threat | Control |
|---|---|
| APK reads/writes Windows files | No shared folders, 9p, virtiofs or USB passthrough are configured. The only host files the VM opens are the read-only system image and its own data disk. |
| Guest reaches host services | The guest's only NIC ends in the core's user-mode stack. The gateway IP is not an alias of host loopback; loopback, link-local (cloud metadata) and multicast destinations are denied; optional LAN block. Unknown ports on gateway IPs are reset. |
| QEMU escape | QEMU is an unmodified upstream build, runs as the (non-admin) user, with a minimal device set (`-nodefaults`), no user networking, and is killed with the app (Job Object). |
| Guest reaches QEMU control | QMP/serial/NIC channels are client connections from QEMU to listeners owned by the core; VNC listens on loopback with a random per-boot password. |

## Hostile traffic and files

- All parsers of untrusted input (TLS ClientHello, DHCP, DNS, WebSocket
  frames, HTTP via Go's stdlib, APK zip/AXML/ARSC, filter expressions) are
  memory-safe Go, bounds-checked, allocation-capped and fuzz-tested.
- Every flow handler runs under panic recovery; a malformed stream cannot
  take the engine down (`Stats.HandlerPanics` counts incidents).
- Captured bodies are served to the UI with `Content-Security-Policy:
  sandbox` and never rendered as live pages; HTML preview uses a sandboxed
  iframe without scripts.
- Captured body size is bounded (`maxBodyMB`); blob paths are content
  hashes (no path traversal) and verified on read.

## HTTPS inspection keys

- A new CA (ECDSA P-256) is generated in memory whenever the sandbox boots
  or a snapshot is restored. Its private key is **never written to disk**,
  never leaves the core process, and is discarded on stop.
- The CA certificate is installed only into the sandbox's system trust store,
  through a tmpfs overlay (not persisted into the disk image). It is never
  installed on Windows.
- Leaf certificates share one per-boot key and are cached in memory.
- Upstream servers are verified against the Windows trust store (plus
  optional configured roots); invalid upstream certificates fail the
  connection exactly as they would for the app.

## Local API

- Bound to 127.0.0.1, random port, 256-bit random token per core start.
- Browser/WebView access: the host opens `/auth?token=…`, which sets an
  HttpOnly SameSite=Strict cookie; tools use `Authorization: Bearer`.
  Tokens are compared in constant time.
- `Host` and `Origin` are validated against the loopback origin (DNS
  rebinding and cross-site request defence); WebSocket origin is checked.
- Strict CSP, `nosniff`, `no-referrer`, `frame-ancestors 'none'`.

## Secrets at rest

- The ADB key (RSA-2048) is encrypted with DPAPI (current user) on Windows.
- Data lives in `%LOCALAPPDATA%\droidpector` (per user, 0700 on other
  OSes). Temporary files (uploaded APKs) are in `tmp\`, emptied at every
  start and deleted on exit.

## Supply chain

- Runtime inputs (QEMU, Android image) are pinned by SHA-256 in
  `tools/runtime/prepare.sh`; the repacked system image is verified
  byte-identical to the original, and its hash is recorded in `runtime.json`.
- `make lint` runs `go vet` and `staticcheck`; `govulncheck` and parser
  fuzzing are part of the release checklist.

## Reporting

Please report vulnerabilities privately to the maintainers (see README) with
reproduction steps; do not open public issues for security problems.
