package device

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// uploadBytes pushes data to a temporary device file and returns its path and
// a cleanup function.
func (d *Device) uploadBytes(ctx context.Context, data []byte) (string, func(), error) {
	remote, err := d.tempName("apkinspector-", ".tmp")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		cctx, cancel := cleanupCtx(ctx)
		defer cancel()
		d.run(cctx, "rm -f "+quote(remote))
	}
	if err := d.sh.Push(ctx, bytes.NewReader(data), int64(len(data)), remote, 0o644, time.Now()); err != nil {
		cleanup()
		return "", nil, err
	}
	return remote, cleanup, nil
}

// ConnectionOwners maps local TCP source ports of guest sockets to the UID
// owning them, from /proc/net/tcp and /proc/net/tcp6. The core uses it to
// attribute captured flows to the app (package) that opened them.
func (d *Device) ConnectionOwners(ctx context.Context) (map[int]int, error) {
	r, err := d.run(ctx, "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null")
	if err != nil {
		return nil, err
	}
	return parseProcNetTCP(r.stdout), nil
}

// parseProcNetTCP parses the kernel's /proc/net/tcp{,6} format:
//
//	sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid ...
//	 0: 0F02000A:A1B2 22D8B85D:01BB 01 00000000:00000000 00:00000000 00000000 10123 ...
func parseProcNetTCP(out string) map[int]int {
	owners := map[int]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || !strings.HasSuffix(f[0], ":") {
			continue
		}
		_, portHex, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		// TIME_WAIT (06) and CLOSE (07) sockets no longer belong to a process
		// and are reported with uid 0; keep an earlier live owner instead.
		if f[3] == "06" || f[3] == "07" {
			continue
		}
		uid, err := strconv.Atoi(f[7])
		if err != nil {
			continue
		}
		owners[int(port)] = uid
	}
	return owners
}

// AppUID converts an Android UID to a readable owner for uids without a
// package (system services).
func AppUID(uid int) string {
	switch {
	case uid == 0:
		return "root"
	case uid == 1000:
		return "system"
	case uid < 10000:
		return fmt.Sprintf("system uid %d", uid)
	}
	return fmt.Sprintf("app uid %d", uid)
}
