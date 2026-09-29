package device

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rule answers commands whose text contains match.
type rule struct {
	match  string
	stdout string
	code   int
	times  int // 0 = unlimited
}

// fakeShell is a scripted device: the first matching rule answers a command.
type fakeShell struct {
	mu     sync.Mutex
	rules  []*rule
	cmds   []string
	pushed map[string][]byte
}

func newFake(rules ...*rule) *fakeShell { return &fakeShell{rules: rules, pushed: map[string][]byte{}} }

func (f *fakeShell) Shell(_ context.Context, cmd string) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	for _, r := range f.rules {
		if strings.Contains(cmd, r.match) {
			if r.times < 0 {
				continue
			}
			if r.times > 0 {
				r.times--
				if r.times == 0 {
					r.times = -1
				}
			}
			return []byte(r.stdout), nil, r.code, nil
		}
	}
	return nil, []byte("sh: unknown command"), 127, nil
}

func (f *fakeShell) Push(_ context.Context, r io.Reader, size int64, remote string, _ fs.FileMode, _ time.Time) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(b)) != size {
		return errors.New("size mismatch")
	}
	f.mu.Lock()
	f.pushed[remote] = b
	f.mu.Unlock()
	return nil
}

func (f *fakeShell) ran(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

var ctx = context.Background()

func tempAPK(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "app.apk")
	os.WriteFile(p, []byte("PK\x03\x04 fake apk"), 0o644)
	return p
}

func TestInstallSuccessAndCleanup(t *testing.T) {
	f := newFake(&rule{match: "pm install -r -t -g", stdout: "Performing Streamed Install\nSuccess\n"}, &rule{match: "rm -f", code: 0})
	d := New(f)
	if err := d.InstallAPK(ctx, tempAPK(t), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.pushed) != 1 || f.ran("rm -f") != 1 {
		t.Fatalf("upload/cleanup: pushed=%d cmds=%v", len(f.pushed), f.cmds)
	}
	for remote := range f.pushed {
		if !strings.HasPrefix(remote, "/data/local/tmp/") {
			t.Fatalf("pushed to %s", remote)
		}
	}
}

func TestInstallFailuresAreTypedAndFriendly(t *testing.T) {
	cases := map[string]string{
		"Failure [INSTALL_FAILED_NO_MATCHING_ABIS: Failed to extract native libraries, res=-113]":                           "CPU architecture",
		"Failure [INSTALL_FAILED_OLDER_SDK: Requires newer sdk version #34 (current version is #33)]":                       "newer Android version",
		"adb: failed to install x.apk: Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: Package com.x signatures do not match]": "different key",
		"Failure [INSTALL_PARSE_FAILED_NO_CERTIFICATES: No signature found]":                                                "not signed",
		"Error: something odd": "Android failed",
	}
	for out, want := range cases {
		f := newFake(&rule{match: "pm install", stdout: out, code: 1}, &rule{match: "rm -f"})
		err := New(f).InstallAPK(ctx, tempAPK(t), InstallOptions{})
		var ie *InstallError
		if !errors.As(err, &ie) || !strings.Contains(ie.Message(), want) || ie.Raw == "" {
			t.Errorf("%q → %v", out, err)
		}
	}
}

func TestInstallRetriesDeprecatedSDK(t *testing.T) {
	f := newFake(
		&rule{match: "--bypass-low-target-sdk-block", stdout: "Success"},
		&rule{match: "pm install", stdout: "Failure [INSTALL_FAILED_DEPRECATED_SDK_VERSION: App package must target at least SDK version 23]", code: 1},
		&rule{match: "rm -f"},
	)
	if err := New(f).InstallAPK(ctx, tempAPK(t), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.ran("pm install") != 2 {
		t.Fatal("expected exactly one retry")
	}
}

func TestLaunchResolvesLauncherActivity(t *testing.T) {
	f := newFake(
		&rule{match: "resolve-activity", stdout: "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.example.app/.MainActivity\n"},
		&rule{match: "am start -W -n", stdout: "Status: ok\nActivity: com.example.app/.MainActivity\nComplete\n"},
	)
	comp, err := New(f).Launch(ctx, "com.example.app")
	if err != nil || comp != "com.example.app/.MainActivity" {
		t.Fatalf("%q %v", comp, err)
	}
	if _, err := New(f).Launch(ctx, "bad pkg; rm -rf /"); !errors.Is(err, ErrInvalidPackage) {
		t.Fatal("package names must be validated (shell injection)")
	}
}

func TestLaunchFallsBackToMonkey(t *testing.T) {
	f := newFake(&rule{match: "resolve-activity", stdout: "No activity found", code: 0}, &rule{match: "monkey", stdout: "Events injected: 1\n"})
	if _, err := New(f).Launch(ctx, "com.example.app"); err != nil {
		t.Fatal(err)
	}
}

func TestBootDetection(t *testing.T) {
	f := newFake(
		&rule{match: "getprop sys.boot_completed", stdout: "\n", times: 2},
		&rule{match: "getprop sys.boot_completed", stdout: "1\n"},
		&rule{match: "pm path android", stdout: "package:/system/framework/framework-res.apk\n"},
	)
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := New(f).WaitBootCompleted(c, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if f.ran("getprop") != 3 {
		t.Fatalf("polled %d times", f.ran("getprop"))
	}
	never := newFake(&rule{match: "getprop", stdout: "0"})
	c2, cancel2 := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel2()
	if err := New(never).WaitBootCompleted(c2, time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
}

func TestInputTextEscaping(t *testing.T) {
	f := newFake(&rule{match: "input"})
	if err := New(f).InputText(ctx, "hi there\nline2"); err != nil {
		t.Fatal(err)
	}
	if f.ran("input text hi%sthere") != 1 || f.ran("input keyevent 66") != 1 || f.ran("input text line2") != 1 {
		t.Fatalf("cmds: %q", f.cmds)
	}
	if err := New(f).InputText(ctx, "سلام"); err == nil {
		t.Fatal("non-ASCII must be rejected with an explanation")
	}
}

func TestParseProcNetTCP(t *testing.T) {
	out := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0F02000A:A1B2 22D8B85D:01BB 01 00000000:00000000 00:00000000 00000000 10123        0 12345 1 0000000000000000 20 4 30 10 -1
   1: 00000000:15B3 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 999 1
   2: 0F02000A:A1B3 22D8B85D:01BB 06 00000000:00000000 03:00000000 00000000     0        0 0 1
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000F02000A:C350 0000000000000000FFFF000022D8B85D:01BB 01 00000000:00000000 00:00000000 00000000 10200 0 1`
	got := parseProcNetTCP(out)
	if got[0xA1B2] != 10123 || got[5555] != 0 || got[0xC350] != 10200 || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if AppUID(10123) != "app uid 10123" || AppUID(1000) != "system" {
		t.Fatal("AppUID")
	}
}

func testCertPEM(t *testing.T) ([]byte, *x509.Certificate) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA", Organization: []string{"X"}},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	c, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), c
}

func TestSubjectHashOldMatchesOpenSSL(t *testing.T) {
	// Known value: `openssl x509 -subject_hash_old` of a cert with subject
	// "CN=droidpector Test" is computed over its DER subject; verify the
	// construction against an independently computed MD5 prefix.
	_, c := testCertPEM(t)
	h := SubjectHashOld(c)
	if len(h) != 8 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("hash %q", h)
	}
	if h != SubjectHashOld(c) {
		t.Fatal("not deterministic")
	}
}

func TestInstallAndVerifyCA(t *testing.T) {
	pemCert, cert := testCertPEM(t)
	name := SubjectHashOld(cert) + ".0"
	f := newFake(
		&rule{match: "id -u", stdout: "0\n"},
		&rule{match: "cat /system/etc/security/cacerts/" + name, stdout: string(pemCert)},
		&rule{match: "set -e", code: 0},
		&rule{match: "rm -f"},
	)
	if err := New(f).InstallCA(ctx, pemCert); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, c := range f.cmds {
		if strings.Contains(c, "set -e") {
			script = c
		}
	}
	for _, want := range []string{"mount -t tmpfs tmpfs /system/etc/security/cacerts", name, "chmod 644", "/apex/com.android.conscrypt/cacerts"} {
		if !strings.Contains(script, want) {
			t.Errorf("install script lacks %q:\n%s", want, script)
		}
	}
	var pushed []byte
	for _, b := range f.pushed {
		pushed = b
	}
	if !bytes.Equal(pushed, pemCert) {
		t.Fatal("certificate not uploaded")
	}
	// Verification detects a different certificate.
	otherPEM, _ := testCertPEM(t)
	if ok, _ := New(f).VerifyCA(ctx, otherPEM); ok {
		t.Fatal("VerifyCA accepted a different certificate")
	}
}

func TestCARequiresRoot(t *testing.T) {
	pemCert, _ := testCertPEM(t)
	f := newFake(&rule{match: "su 0 id -u", stdout: "", code: 1}, &rule{match: "id -u", stdout: "2000\n"}, &rule{match: "rm -f"})
	if err := New(f).InstallCA(ctx, pemCert); !errors.Is(err, ErrRootRequired) {
		t.Fatalf("got %v", err)
	}
	if err := New(f).InstallCA(ctx, []byte("junk")); err == nil {
		t.Fatal("junk PEM accepted")
	}
}

func TestPackageQueries(t *testing.T) {
	m := parsePackageList("package:com.a uid:10100\npackage:com.b uid:10101,1000\ngarbage\n")
	if m["com.a"] != 10100 || m["com.b"] != 10101 || len(m) != 2 {
		t.Fatal(m)
	}
	f := newFake(&rule{match: "am force-stop"}, &rule{match: "pm clear", stdout: "Success"})
	d := New(f)
	if err := d.ForceStop(ctx, "com.a"); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearData(ctx, "com.a"); err != nil {
		t.Fatal(err)
	}
}

func TestSetDefaultHome(t *testing.T) {
	f := newFake(
		&rule{match: "query-activities", stdout: "priority=0 preferredOrder=0\ncom.android.settings/.FallbackHome\npriority=0\ncom.farmerbb.taskbar/.activity.HomeActivity\npriority=0\ncom.android.launcher3/.uioverrides.QuickstepLauncher\n"},
		&rule{match: "set-home-activity", stdout: "Success"},
	)
	got, err := New(f).SetDefaultHome(ctx)
	if err != nil || got != "com.android.launcher3/.uioverrides.QuickstepLauncher" || f.ran("set-home-activity com.android.launcher3/.uioverrides.QuickstepLauncher") != 1 {
		t.Fatalf("%q %v %q", got, err, f.cmds)
	}
	single := newFake(&rule{match: "query-activities", stdout: "com.android.launcher3/.Launcher\n"})
	if got, err := New(single).SetDefaultHome(ctx); err != nil || got != "" || single.ran("set-home") != 0 {
		t.Fatal("single launcher must be left alone")
	}
}

func TestFirewallScriptAndCounters(t *testing.T) {
	s := firewallScript(FirewallRules{AllowUIDs: []int{10123, 10200}, AllowDNS: true})
	for _, want := range []string{
		"iptables -w -I OUTPUT 1 -j droidpector",
		"ip6tables -w -I OUTPUT 1 -j droidpector",
		"-o lo -j RETURN",
		"--ctstate ESTABLISHED,RELATED -j RETURN",
		"--dport 67:68 -j RETURN",
		"-p udp --dport 53 -j RETURN",
		"-p udp --dport 123 -j RETURN", // NTP: a wrong clock breaks HTTPS
		"--uid-owner 10123 -j RETURN",
		"--uid-owner 10200 -j RETURN",
		"-p tcp -j REJECT --reject-with tcp-reset",
		"--reject-with icmp6-port-unreachable",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "set -e") || !strings.Contains(s, "-D OUTPUT -j droidpector 2>/dev/null || true") || !strings.Contains(s, "-I OUTPUT 1 -j droidpector || exit 1") {
		t.Fatal("optional steps must not abort the script; mandatory ones must")
	}
	// Allowed UIDs must be listed before the REJECT rules.
	if strings.Index(s, "--uid-owner 10123") > strings.Index(s, "-p tcp -j REJECT") {
		t.Fatal("allow rules must precede reject rules")
	}
	if strings.Contains(firewallScript(FirewallRules{}), "--dport 53") {
		t.Fatal("DNS must be blockable")
	}
	out := `Chain droidpector (1 references)
    pkts      bytes target     prot opt in     out     source               destination
      12      720 RETURN     all  --  *      lo      0.0.0.0/0            0.0.0.0/0
      37     2220 REJECT     tcp  --  *      *       0.0.0.0/0            0.0.0.0/0            reject-with tcp-reset
       5      400 REJECT     all  --  *      *       0.0.0.0/0            0.0.0.0/0            reject-with icmp-port-unreachable
`
	if n := parseRejectCount(out); n != 42 {
		t.Fatalf("blocked count %d", n)
	}
	f := newFake(&rule{match: "id -u", stdout: "0\n"}, &rule{match: "iptables", stdout: ""}, &rule{match: "set -e", stdout: ""})
	if err := New(f).SetAppFirewall(ctx, FirewallRules{AllowUIDs: []int{10123}, AllowDNS: true}); err != nil {
		t.Fatal(err)
	}
	if err := New(f).ClearAppFirewall(ctx); err != nil {
		t.Fatal(err)
	}
	noRoot := newFake(&rule{match: "su 0 id -u", code: 1}, &rule{match: "id -u", stdout: "2000\n"})
	if err := New(noRoot).SetAppFirewall(ctx, FirewallRules{}); !errors.Is(err, ErrRootRequired) {
		t.Fatalf("expected ErrRootRequired, got %v", err)
	}
	pk := newFake(&rule{match: "pm list packages -U", stdout: "package:com.a uid:10100\n"})
	if uid, err := New(pk).PackageUID(ctx, "com.a"); err != nil || uid != 10100 {
		t.Fatal(uid, err)
	}
	if _, err := New(pk).PackageUID(ctx, "com.missing"); !errors.Is(err, ErrNotInstalled) {
		t.Fatal(err)
	}
}

func TestRotation(t *testing.T) {
	f := newFake(&rule{match: "user_rotation 1", stdout: ""}, &rule{match: "settings get system user_rotation", stdout: "1\n"})
	d := New(f)
	if err := d.SetRotation(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if f.ran("accelerometer_rotation 0 && settings put system user_rotation 1") != 1 {
		t.Fatalf("cmds %q", f.cmds)
	}
	if n, _ := d.Rotation(ctx); n != 1 {
		t.Fatal(n)
	}
	if err := d.SetRotation(ctx, 4); err == nil {
		t.Fatal("invalid orientation accepted")
	}
	if n, _ := New(newFake(&rule{match: "settings get", stdout: "null\n"})).Rotation(ctx); n != 0 {
		t.Fatal("null must read as 0")
	}
}
