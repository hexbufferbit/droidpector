package adb

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandshakeNoAuth(t *testing.T) {
	c := connect(t, newFake(), Config{})
	info := c.Info()
	if info.State != "device" || info.Props["ro.product.model"] != "BlissOS" {
		t.Errorf("info = %+v", info)
	}
	if !info.HasFeature("shell_v2") || !info.HasFeature("abb") || info.HasFeature("nope") {
		t.Errorf("features = %v", info.FeatureList())
	}
	if info.MaxPayload != DefaultMaxPayload || info.Version != Version {
		t.Errorf("negotiated payload %d version %#x", info.MaxPayload, info.Version)
	}
}

func TestHandshakeAuthorizedKey(t *testing.T) {
	key := testKey(t)
	d := newFake(func(d *fakeADBD) { d.requireAuth = true; d.auth = []*rsa.PublicKey{&key.PublicKey} })
	connect(t, d, Config{Key: key})
	if len(d.offers) != 0 {
		t.Error("public key offered although the signature was accepted")
	}
}

func TestHandshakeOffersPublicKey(t *testing.T) {
	key := testKey(t)
	d := newFake(func(d *fakeADBD) { d.requireAuth = true; d.acceptNewKey = true })
	connect(t, d, Config{Key: key})
	if len(d.offers) != 1 || !strings.HasSuffix(d.offers[0], " "+DefaultKeyComment+"\x00") {
		t.Fatalf("offers = %q", d.offers)
	}
	pub, err := DecodeAndroidPublicKey(d.offers[0])
	if err != nil || !pub.Equal(&key.PublicKey) {
		t.Fatalf("offered key does not round-trip: %v", err)
	}
}

func TestHandshakeAuthRequired(t *testing.T) {
	key := testKey(t)
	t.Run("never accepted", func(t *testing.T) {
		d := newFake(func(d *fakeADBD) { d.requireAuth = true })
		nc := d.start(t)
		_, err := NewConn(context.Background(), nc, Config{Key: key, AuthTimeout: 50 * time.Millisecond})
		if !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("err = %v, want ErrAuthRequired", err)
		}
	})
	t.Run("no key", func(t *testing.T) {
		d := newFake(func(d *fakeADBD) { d.requireAuth = true })
		nc := d.start(t)
		if _, err := NewConn(context.Background(), nc, Config{}); !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("err = %v, want ErrAuthRequired", err)
		}
	})
	t.Run("context cancelled", func(t *testing.T) {
		d := newFake(func(d *fakeADBD) { d.requireAuth = true })
		nc := d.start(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewConn(ctx, nc, Config{Key: key}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestConnectDialError(t *testing.T) {
	boom := errors.New("boom")
	_, err := Connect(context.Background(), Config{Dial: func(context.Context) (net.Conn, error) { return nil, boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Connect(context.Background(), Config{}); err == nil {
		t.Fatal("missing Dial accepted")
	}
}

func scriptedShell(cmd string) (string, string, int) {
	switch {
	case cmd == "getprop sys.boot_completed":
		return "1\n", "", 0
	case cmd == "false":
		return "", "", 1
	case cmd == "big":
		return strings.Repeat("0123456789", 100_000), "", 0
	case strings.HasPrefix(cmd, "exit "):
		var n int
		fmt.Sscanf(cmd, "exit %d", &n)
		return "", "", n
	case strings.HasPrefix(cmd, "echo "):
		return strings.TrimPrefix(cmd, "echo ") + "\n", "", 0
	}
	return "", "/system/bin/sh: " + cmd + ": inaccessible or not found\n", 127
}

func TestShellV2(t *testing.T) {
	c := connect(t, newFake(func(d *fakeADBD) { d.shell = scriptedShell }), Config{})
	ctx := context.Background()
	cases := []struct {
		cmd         string
		out, errOut string
		code        int
	}{
		{"getprop sys.boot_completed", "1\n", "", 0},
		{"false", "", "", 1},
		{"exit 255", "", "", 255},
		{"nosuchcmd", "", "/system/bin/sh: nosuchcmd: inaccessible or not found\n", 127},
	}
	for _, tc := range cases {
		out, errOut, code, err := c.Shell(ctx, tc.cmd)
		if err != nil || string(out) != tc.out || string(errOut) != tc.errOut || code != tc.code {
			t.Errorf("Shell(%q) = %q, %q, %d, %v", tc.cmd, out, errOut, code, err)
		}
	}
	out, _, _, err := c.Shell(ctx, "big")
	if err != nil || len(out) != 1_000_000 {
		t.Errorf("big output: %d bytes, %v", len(out), err)
	}
}

func TestShellLegacy(t *testing.T) {
	// Old protocol version: checksums are required in both directions and
	// shell_v2 is unavailable.
	d := newFake(func(d *fakeADBD) {
		d.shell = scriptedShell
		d.version = versionMin
		d.maxPayload = legacyMaxPayload
		d.banner = "device::ro.product.model=Old;features=cmd"
	})
	c := connect(t, d, Config{})
	if c.Info().Version != versionMin || c.Info().MaxPayload != legacyMaxPayload {
		t.Fatalf("negotiated %+v", c.Info())
	}
	out, errOut, code, err := c.Shell(context.Background(), "exit 3")
	if err != nil || code != 3 || len(out) != 0 || errOut != nil {
		t.Errorf("exit 3: %q %q %d %v", out, errOut, code, err)
	}
	out, _, code, err = c.Shell(context.Background(), "echo hello")
	if err != nil || code != 0 || string(out) != "hello\n" {
		t.Errorf("echo: %q %d %v", out, code, err)
	}
	// Large output crosses many 4 KiB packets.
	out, _, code, err = c.Shell(context.Background(), "big")
	if err != nil || code != 0 || len(out) != 1_000_000 {
		t.Errorf("big: %d %d %v", len(out), code, err)
	}
}

func TestExec(t *testing.T) {
	c := connect(t, newFake(), Config{})
	s, err := c.Exec(context.Background(), "echo hi there")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "hi there\n" {
		t.Fatalf("exec: %q %v", b, err)
	}
}

func TestServiceRejected(t *testing.T) {
	c := connect(t, newFake(), Config{})
	if _, err := c.Open(context.Background(), "bogus:"); !errors.Is(err, ErrServiceRejected) {
		t.Fatalf("err = %v", err)
	}
}

func TestPushPullStat(t *testing.T) {
	d := newFake()
	c := connect(t, d, Config{})
	ctx := context.Background()
	data := make([]byte, 3*syncDataMax*5+12345) // many DATA chunks and WRTE packets
	for i := range data {
		data[i] = byte(i * 7)
	}
	mtime := time.Unix(1_700_000_000, 0)
	if err := c.Push(ctx, bytes.NewReader(data), int64(len(data)), "/data/local/tmp/x.apk", 0o644, mtime); err != nil {
		t.Fatal(err)
	}
	f := d.files["/data/local/tmp/x.apk"]
	if !bytes.Equal(f.data, data) || f.mode != 0o100644 || f.mtime != uint32(mtime.Unix()) {
		t.Fatalf("pushed file: %d bytes mode %o mtime %d", len(f.data), f.mode, f.mtime)
	}
	fi, err := c.Stat(ctx, "/data/local/tmp/x.apk")
	if err != nil || fi.Size != int64(len(data)) || !fi.IsRegular() || fi.IsDir() || fi.Perm() != 0o644 || !fi.ModTime.Equal(mtime) {
		t.Fatalf("stat: %+v %v", fi, err)
	}
	var got bytes.Buffer
	n, err := c.Pull(ctx, "/data/local/tmp/x.apk", &got)
	if err != nil || n != int64(len(data)) || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("pull: %d %v", n, err)
	}
	// Empty file.
	if err := c.Push(ctx, bytes.NewReader(nil), 0, "/data/local/tmp/empty", 0o600, mtime); err != nil {
		t.Fatal(err)
	}
	got.Reset()
	if n, err := c.Pull(ctx, "/data/local/tmp/empty", &got); err != nil || n != 0 {
		t.Fatalf("pull empty: %d %v", n, err)
	}
}

func TestSyncErrors(t *testing.T) {
	c := connect(t, newFake(), Config{})
	ctx := context.Background()
	if _, err := c.Stat(ctx, "/nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat missing: %v", err)
	}
	var se *SyncError
	if _, err := c.Pull(ctx, "/nope", io.Discard); !errors.As(err, &se) || !strings.Contains(se.Message, "No such file") {
		t.Errorf("pull missing: %v", err)
	}
	err := c.Push(ctx, strings.NewReader("abc"), 3, "/readonly/x", 0o644, time.Now())
	if !errors.As(err, &se) || se.Op != "push" {
		t.Errorf("push read-only: %v", err)
	}
	if err := c.Push(ctx, strings.NewReader("ab"), 3, "/data/short", 0o644, time.Now()); err == nil || !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("short reader: %v", err)
	}
	if err := c.Push(ctx, strings.NewReader(""), 0, "", 0o644, time.Now()); err == nil {
		t.Error("empty path accepted")
	}
	// The connection must still be usable after failed operations.
	if _, _, code, err := c.Shell(ctx, "false"); err != nil || code != 127 {
		t.Errorf("shell after errors: %d %v", code, err)
	}
}

func TestConcurrentStreams(t *testing.T) {
	d := newFake(func(d *fakeADBD) { d.shell = scriptedShell })
	c := connect(t, d, Config{})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("n%d\n", i)
			out, _, code, err := c.Shell(ctx, fmt.Sprintf("echo n%d", i))
			if err != nil || code != 0 || string(out) != want {
				errs <- fmt.Errorf("shell %d: %q %d %v", i, out, code, err)
			}
		}()
		go func() {
			defer wg.Done()
			data := bytes.Repeat([]byte{byte(i)}, 100_000+i)
			p := fmt.Sprintf("/data/local/tmp/f%d", i)
			if err := c.Push(ctx, bytes.NewReader(data), int64(len(data)), p, 0o644, time.Now()); err != nil {
				errs <- err
				return
			}
			var got bytes.Buffer
			if _, err := c.Pull(ctx, p, &got); err != nil || !bytes.Equal(got.Bytes(), data) {
				errs <- fmt.Errorf("pull %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestPeerDisconnect(t *testing.T) {
	d := newFake()
	c := connect(t, d, Config{})
	res := make(chan error, 1)
	go func() {
		_, _, _, err := c.Shell(context.Background(), "hang")
		res <- err
	}()
	// Once the device is running the command, drop the transport.
	<-d.hung
	d.conn.Close()
	if err := <-res; !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("shell err = %v, want ErrConnectionLost", err)
	}
	<-c.Done()
	if !errors.Is(c.Err(), ErrConnectionLost) {
		t.Errorf("Err() = %v", c.Err())
	}
	if _, err := c.Open(context.Background(), "shell:true"); !errors.Is(err, ErrConnectionLost) {
		t.Errorf("open after loss: %v", err)
	}
}

func TestContextCancelAndClose(t *testing.T) {
	d := newFake()
	c := connect(t, d, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		_, _, _, err := c.Shell(ctx, "hang")
		res <- err
	}()
	<-d.hung
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// The transport survives a cancelled stream.
	if _, _, _, err := c.Shell(context.Background(), "x"); err != nil {
		t.Fatalf("shell after cancel: %v", err)
	}
	c.Close()
	c.Close() // idempotent
	if _, _, _, err := c.Shell(context.Background(), "x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("shell after close: %v", err)
	}
}

func TestStreamReadAfterRemoteClose(t *testing.T) {
	c := connect(t, newFake(), Config{})
	s, err := c.Exec(context.Background(), "echo data")
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(s)
	if err != nil || string(b) != "data\n" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Error("write after remote close succeeded")
	}
	s.Close()
	if _, err := s.Read(make([]byte, 1)); !errors.Is(err, ErrClosed) {
		t.Errorf("read after close: %v", err)
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestKeyHelpers(t *testing.T) {
	key := testKey(t)
	p, err := EncodePrivateKeyPEM(key)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DecodePrivateKeyPEM(p)
	if err != nil || !k2.Equal(key) {
		t.Fatalf("PEM round trip: %v", err)
	}
	if _, err := DecodePrivateKeyPEM([]byte("junk")); err == nil {
		t.Error("junk accepted")
	}
	pub, err := EncodeAndroidPublicKey(&key.PublicKey, "me@host")
	if err != nil || !strings.HasSuffix(pub, " me@host") {
		t.Fatalf("encode: %q %v", pub, err)
	}
	back, err := DecodeAndroidPublicKey(pub)
	if err != nil || !back.Equal(&key.PublicKey) {
		t.Fatalf("decode: %v", err)
	}
	if _, err := DecodeAndroidPublicKey("QUJD"); err == nil {
		t.Error("short key accepted")
	}
	token := bytes.Repeat([]byte{7}, tokenSize)
	sig, err := SignToken(key, token)
	if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, token, sig) != nil {
		t.Fatalf("SignToken: %v", err)
	}
	if _, err := SignToken(key, []byte("short")); err == nil {
		t.Error("short token accepted")
	}
}

// TestAndroidPublicKeyLayout checks the mincrypt struct against values
// computed independently: n0inv * n[0] == -1 mod 2^32 and rr == 2^4096 mod n.
func TestAndroidPublicKeyLayout(t *testing.T) {
	key := testKey(t)
	pub, _ := EncodeAndroidPublicKey(&key.PublicKey, "")
	raw := mustB64(t, pub)
	if len(raw) != 524 {
		t.Fatalf("struct size %d", len(raw))
	}
	le := func(b []byte) uint32 { return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24 }
	if le(raw) != 64 {
		t.Errorf("len = %d", le(raw))
	}
	n0inv, n0 := le(raw[4:]), le(raw[8:])
	if n0inv*n0 != 0xFFFFFFFF {
		t.Errorf("n0inv*n0 = %#x", n0inv*n0)
	}
	if le(raw[520:]) != uint32(key.PublicKey.E) {
		t.Errorf("exponent = %d", le(raw[520:]))
	}
}

func TestParseBanner(t *testing.T) {
	cases := []struct {
		in, state, model string
		feats            []string
	}{
		{"device::ro.product.name=a;ro.product.model=M;ro.product.device=d;features=shell_v2,cmd\x00", "device", "M", []string{"shell_v2", "cmd"}},
		{"device::", "device", "", nil},
		{"recovery::ro.product.model=R;", "recovery", "R", nil},
		{"device:serial123:ro.product.model=L", "device", "L", nil},
		{"", "", "", nil},
	}
	for _, tc := range cases {
		state, props, feats := parseBanner(tc.in)
		if state != tc.state || props["ro.product.model"] != tc.model || len(feats) != len(tc.feats) {
			t.Errorf("parseBanner(%q) = %q %v %v", tc.in, state, props, feats)
		}
		for _, f := range tc.feats {
			if !feats[f] {
				t.Errorf("parseBanner(%q) missing feature %s", tc.in, f)
			}
		}
	}
}

func TestReadMessageValidation(t *testing.T) {
	good := Message{Command: CmdWRTE, Arg0: 1, Arg1: 2, Data: []byte("hello")}.marshal(true)
	if m, err := readMessage(bytes.NewReader(good), 1024, checksumRequired); err != nil || string(m.Data) != "hello" {
		t.Fatalf("good: %v", err)
	}
	bad := bytes.Clone(good)
	bad[20] ^= 1
	if _, err := readMessage(bytes.NewReader(bad), 1024, checksumIgnore); !errors.Is(err, ErrProtocol) {
		t.Errorf("bad magic: %v", err)
	}
	bad = bytes.Clone(good)
	bad[24] ^= 1
	if _, err := readMessage(bytes.NewReader(bad), 1024, checksumRequired); !errors.Is(err, ErrProtocol) {
		t.Errorf("bad checksum: %v", err)
	}
	if _, err := readMessage(bytes.NewReader(bad), 1024, checksumIgnore); err != nil {
		t.Errorf("checksum ignored: %v", err)
	}
	noSum := Message{Command: CmdWRTE, Data: []byte("x")}.marshal(false)
	if _, err := readMessage(bytes.NewReader(noSum), 1024, checksumIfPresent); err != nil {
		t.Errorf("zero checksum: %v", err)
	}
	if _, err := readMessage(bytes.NewReader(good), 2, checksumIgnore); !errors.Is(err, ErrProtocol) {
		t.Errorf("oversized: %v", err)
	}
	if _, err := readMessage(bytes.NewReader(good[:26]), 1024, checksumIgnore); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated: %v", err)
	}
}

func FuzzReadMessage(f *testing.F) {
	f.Add(Message{Command: CmdCNXN, Arg0: Version, Arg1: DefaultMaxPayload, Data: []byte("device::features=shell_v2")}.marshal(true))
	f.Add(Message{Command: CmdWRTE, Arg0: 1, Arg1: 2}.marshal(false))
	f.Add([]byte("garbage that is not a packet at all"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for i := 0; i < 8; i++ {
			m, err := readMessage(r, 4096, checksumIfPresent)
			if err != nil {
				return
			}
			if len(m.Data) > 4096 {
				t.Fatalf("payload %d exceeds limit", len(m.Data))
			}
			again, err := readMessage(bytes.NewReader(m.marshal(true)), 4096, checksumRequired)
			if err != nil || again.Command != m.Command || !bytes.Equal(again.Data, m.Data) {
				t.Fatalf("re-encode mismatch: %v", err)
			}
			parseBanner(string(m.Data))
		}
	})
}
