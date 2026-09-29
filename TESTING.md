# Testing

No test depends on the Internet: traffic goes to the deterministic test
server (`src/testserver`), reached from the guest through explicit host
mappings.

## Levels

| Level | Command | What it covers |
|---|---|---|
| Unit | `go test ./src/...` | model, URL/query/header parsing, content decoding & classification, filter engine (+fuzz), query service (+100k benchmark), cURL (+shell round-trip), HAR, storage (blobs, writer backpressure, retention, migrations), ClientHello/DHCP/WebSocket parsers (+fuzz), CA, policy, QMP client, console protocol, QEMU args, accelerator selection, error classification, RFB client and streamer, ADB protocol (fake adbd), APK/AXML/ARSC parser (+fuzz), device operations, APK builder/v2 signer |
| Integration (no VM) | `go test ./tests/integration/` | simulated guest ↔ real gateway ↔ test server: DHCP, DNS (A/NXDOMAIN/AAAA), HTTP, HTTPS over HTTP/1.1 and HTTP/2, the method/status/content matrix, large/truncated/binary/gzip bodies, slow/reset/timeout, WebSocket frames, pinning → passthrough, host-protection policy, raw TCP, replay, host-traffic isolation, 200 concurrent requests |
| API | `go run ./tools/devserver` + curl (scripted in CI via Playwright) | real core API: sessions, events paging/filter, detail, bodies, cURL, replay, HAR, auth |
| UI | `cd src/ui/web && npm test` | components, keysym mapping, formatters, hex dump, JSON tree, virtual list math |
| UI E2E | `make e2e` | Playwright against the real core (devserver): request appears → details → Copy as cURL, filters, JSON/image viewers, WebSocket messages, encrypted TLS, replay, 500-row virtualization, HAR export, raw-TCP protocol label, app-only toggle round-trip |
| VM integration | `make test-vm` | real QEMU + Android 13 with the portrait phone display: boot, ADB (root), CA install, TestApp install/launch, HTTPS event attributed to the TestApp package, app-only firewall (system-uid connection rejected, `blockedFlows` grows, toggle off/on), rotation (`user_rotation`), button clicks through the display stream in landscape and portrait (validates the UI's pointer mapping), replay, crash (QEMU killed) → recovery with the app preserved, quick-start restart |
| Windows smoke | `tests/e2e/windows-smoke.ps1` (run on a Windows machine) | offline silent install of the single installer on a clean runner, files/registry, bundled QEMU runs, core API/auth, Android boots, VM dies with the app, clean silent uninstall |

## Test matrix coverage

Methods GET/POST/PUT/PATCH/DELETE; bodies JSON/text/binary/HTML/XML/image,
small/large (5 MiB, truncated at the capture limit); statuses 200, 201, 204,
301, 400, 401, 403, 404, 500; slow response, connection reset, client
timeout; HTTP, HTTPS (h1/h2), WebSocket, DNS — all in
`tests/integration/network_test.go`.

## TestApp

`tools/testapp` (Java, no SDK): buttons GET, POST, JSON, Image, Error,
WebSocket against `https://test.apkinspector.internal`; `am start … --es action
<name>` triggers a button (the E2E suite also taps buttons through the
display). Build with `make testapp` (javac → d8 → `tools/apkbuild`).

## Running the VM suite

```bash
make runtime testapp
make test-vm                     # or:
APKINSPECTOR_RUNTIME_DIR=$PWD/build/runtime APKINSPECTOR_TESTAPP=$PWD/build/testapp/TestApp.apk \
  go test -tags vm -timeout 120m -v ./tests/vm/
```

Linux CI enables `/dev/kvm`; on macOS/Apple Silicon the guest runs under TCG
(slower but functional).

## Static analysis

`make lint`: gofmt, `go vet` (host and `GOOS=windows`), staticcheck, UI
typecheck and ESLint. Before a release also run `govulncheck ./...` and the
fuzz targets (`go test -fuzz=… -fuzztime=30s`) for the parsers.
