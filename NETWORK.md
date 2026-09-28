# Network — isolation, capture and inspection

## Topology

```
Android guest (10.0.2.15)
  virtio-net ──(QEMU "socket" netdev: 32-bit BE length + Ethernet frame)──► core
                                                                          │
                                         gVisor netstack (user mode) ◄────┘
                                         NIC 1: 10.0.2.2 (gateway, DHCP), 10.0.2.3 (DNS)
                                         promiscuous + spoofing: accepts every flow
                                                     │
      ┌───────────────┬──────────────┬───────────────┼───────────────────┐
   DHCP (:67)     DNS (:53 UDP/TCP)  TCP forwarder   UDP forwarder
                   answered on host  │               QUIC (UDP/443) → ICMP unreachable
                   + DNS event       │               others → host UDP NAT + UDP event
                                     ▼
                         Policy check → upstream dial (host socket)
                                     │
                     sniff first bytes (2 s, then server-first → raw relay)
          ┌──────────────────────────┼───────────────────────────┐
     0x16 TLS ClientHello       HTTP/1.x request line        anything else
     (SNI, ALPN)                 → HTTP proxy (capture)       → raw relay + TCP event
          │
   CA active? ALPN is HTTP? host not marked pinned?
     yes → upstream TLS handshake FIRST (learn ALPN) → client handshake
           with leaf for SNI → HTTP/1.1 or HTTP/2 proxy (capture)
     no  → passthrough relay + TLS event (SNI, bytes, "encrypted")
   client rejects our certificate → host marked pinned for this boot,
   event "HTTPS encrypted traffic detected. Payload inspection unavailable…"
```

**Isolation:** only frames from QEMU's socket link enter the stack. Windows
traffic has no path into it, and the guest has no other NIC (the QEMU command
line is unit-tested to contain exactly one `-netdev`, and never `user`/`hostfwd`).

**ADB** traffic is originated *by* the core from inside the stack to
10.0.2.15:5555 (`Stack.DialGuest`), so it never passes the forwarder and is
never captured or exposed on a host port.

## Policy (host protection)

Default: deny loopback, link-local (incl. cloud metadata 169.254.169.254),
multicast; the gateway addresses answer only DHCP/DNS (other ports are reset —
e.g. DNS-over-TLS on 853 so Android falls back to observable DNS). Optional:
block private (LAN) networks, allow QUIC. Unlike QEMU slirp, 10.0.2.2 is **not**
an alias of host loopback. Replays go through the same policy.

**Host mappings** (config `hostMappings: ["host=ip:port"]`) resolve a hostname
to a virtual IP from 198.18.0.0/15 and dial the configured target; used by
tests (deterministic test server) and for "map remote" debugging. They are
explicit and off by default.

## HTTP capture

The gateway serves each intercepted connection with Go's `net/http` server
(HTTP/1.1) or `x/net/http2` server (h2) and forwards through a per-connection
transport that reuses the already-established upstream connection first.
Hop-by-hop headers are removed, `Accept-Encoding` is passed through untouched
(bodies are stored as transmitted and decoded on view), Go's default
User-Agent is suppressed, streaming responses are flushed immediately (SSE).
Upstream failures are mirrored to the app by dropping the connection (no
synthetic responses). Events are emitted when request headers arrive
(`pending`) and on completion (`complete`/`error`).

Bodies: captured up to `maxBodyMB` (default 10 MiB) per body; the traffic
itself is never limited. `BodyRef{hash,size,stored,truncated,encoding}`.

Timing (HAR-compatible): DNS (from the guest's preceding lookup), connect and
TLS (first request of a connection), send, wait (TTFB), receive.

## WebSocket

`Upgrade: websocket` exchanges are completed with a raw 101, then relayed
byte-for-byte while two passive RFC 6455 parsers (one per direction) record
frames: masking, fragmentation, control frames, and `permessage-deflate`
(context takeover emulated with a 32 KiB dictionary). Frames > 64 KiB are
truncated in the record only. Parser errors stop recording, never the relay.

## DNS

Guest queries (UDP and TCP, any destination IP) are answered on the host:
A (system resolver, honours hosts file/VPN), AAAA (empty — IPv4-only
sandbox), TXT/MX/SRV/CNAME via the system resolver, HTTPS/SVCB/others NODATA.
Every query becomes a `dns` event; answers feed an IP→hostname cache used to
name raw TCP/TLS flows and attribute DNS time to requests.

## Event model

`model.Event` (see `src/model/event.go`): id (ULID), session, seq, kind
(http, websocket, dns, tls, tcp, udp), category (api, document, image,
media, script, style, font, websocket, dns, other), state, initiator
(guest/replay), timestamps/duration, package (from `/proc/net/tcp*` socket
ownership), protocol, method, scheme, host, port, path, query, status, mime,
sizes, headers, body refs, timing, TLS info (SNI, version, cipher, ALPN,
intercepted, passthrough reason, server chain), connection, DNS, WS frames,
`encrypted`, error. The capture layer only emits events to a `Sink`; storage,
query and UI never import the network package.

## Storage

SQLite (WAL): `sessions`, `events` (indexed summary columns), `event_details`
(JSON: headers, timing, TLS, DNS…), `ws_frames`. Bodies: `blobs/aa/<sha256>`
(zstd when smaller by ≥10 %), integrity-checked on read, garbage-collected
after session deletion. The async writer batches upserts (≤512 per
transaction, ≤50 ms latency), coalesces pending→complete snapshots, bounds its
queue (8192) and drops (counting) instead of stalling capture. Retention:
unsaved sessions beyond `keepSessions`/`keepDays` are deleted.

## Filter language

```
api.example.com                 free text (URL contains)
host:api.example.com            exact (case-insensitive) or glob host:*.example.com
host contains "example.com"     contains / startswith / endswith / equals / matches
method:POST status:401          implicit AND;  OR, NOT, -negation, (grouping)
status:4xx  status>=400         classes and comparisons
path:/v1/  mime:json  scheme:https  type:image  kind:dns  package:com.x
size>10k  duration>=500ms       units: b,k,kb,m,mb / ms,s,m
is:error is:pending is:encrypted is:replay is:https
path:/^\/v[0-9]+\//             regular expressions
```

Quick filters: All, XHR/API, Documents, Images, Media, WebSocket, DNS, Other.
The query service keeps an in-memory index of the viewed sessions; filtering +
sorting + paging 100k rows takes ~4 ms (benchmark in `src/query`).

## Replay, cURL, HAR

- Replay re-sends the stored request from the host through the policy and
  records a **new** event (`initiator=replay`, `replayOf=<id>`); requests with
  truncated bodies are refused rather than replayed incorrectly.
- cURL: POSIX-shell quoting (verified by a shell round-trip test), `-X`,
  headers (without hop-by-hop / pseudo headers), `--compressed`,
  `--data-raw` for text and ANSI-C `$'…'` for binary bodies. Generators are
  pluggable (`export.Generator`) for Python/JS/Go.
- HAR 1.2 streamed export with decoded content (text or base64), cookies,
  query string, postData/params, timings, server IP, `_webSocketMessages`.
  DNS/raw TCP/uninspected TLS have no HAR representation and are omitted.

## Known limitations

- QUIC/HTTP3 is blocked by default so apps fall back to TCP (configurable).
- Apps that pin certificates or ship their own trust store are shown as
  encrypted connections (metadata only) — by design.
- HTTP/2 WebSockets (RFC 8441) and HTTP/2 cleartext (h2c) are relayed
  opaquely.
- Header order/case of HTTP/1 requests is normalized (canonical names,
  alphabetical display, like DevTools).
