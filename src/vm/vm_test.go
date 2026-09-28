package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testProfile(t *testing.T) Profile {
	dir := t.TempDir()
	for _, f := range []string{"kernel", "initrd.img", "android.iso", "data-template.qcow2"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	return Profile{Name: ProfileX86_64, QEMU: "qemu-system-x86_64", Machine: "q35", Kernel: "kernel", Initrd: "initrd.img",
		ISO: "android.iso", DataTemplate: "data-template.qcow2", Cmdline: "root=/dev/ram0 console=ttyS0", Dir: dir}
}

func joined(args []string) string { return strings.Join(args, " ") }

func TestBuildArgs(t *testing.T) {
	p := testProfile(t)
	spec := LaunchSpec{Profile: p, Accel: "whpx", MemoryMB: 4096, CPUs: 4, DataDisk: `C:\Users\x,y\data.qcow2`,
		QMPAddr: "127.0.0.1:4001", SerialAddr: "127.0.0.1:4002", NetAddr: "127.0.0.1:4003", VNCPort: 5911, SerialLog: "/logs/serial.log", LoadVM: "boot"}
	args, err := BuildArgs(spec)
	if err != nil {
		t.Fatal(err)
	}
	s := joined(args)
	for _, want := range []string{
		"-nodefaults", "-machine q35", "-accel whpx,kernel-irqchip=off", "-smp 4", "-m 4096M",
		"-append root=/dev/ram0 console=ttyS0",
		`filename=C:\Users\x,,y\data.qcow2`, // commas escaped for QEMU option syntax
		"node-name=data,", "-netdev socket,id=net0,connect=127.0.0.1:4003", "virtio-net-pci,netdev=net0",
		"-vnc 127.0.0.1:11,to=211,password=on", "-display none", "-device VGA,edid=on,xres=720,yres=1280",
		"-chardev socket,id=qmp,host=127.0.0.1,port=4001,server=off", "-mon chardev=qmp,mode=control",
		"-chardev socket,id=con,host=127.0.0.1,port=4002,server=off,logfile=/logs/serial.log,logappend=on",
		"-serial chardev:con", "-loadvm boot", "usb-tablet,bus=xhci.0,id=tablet0", "read-only=on",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("args lack %q\n%s", want, s)
		}
	}
	// Exactly one NIC, and no user-mode (slirp) networking that would bypass the gateway.
	if strings.Count(s, "-netdev") != 1 || strings.Contains(s, "user,") || strings.Contains(s, "hostfwd") {
		t.Fatalf("unexpected networking: %s", s)
	}
	spec.Profile.Display = Display{Width: 1080, Height: 1920}
	args, _ = BuildArgs(spec)
	if !strings.Contains(joined(args), "xres=1080,yres=1920") {
		t.Error("custom display geometry not applied")
	}
	spec.Accel = "tcg"
	args, _ = BuildArgs(spec)
	if !strings.Contains(joined(args), "-accel tcg,thread=multi") {
		t.Error("tcg accel")
	}
	spec.Accel = "bogus"
	if _, err := BuildArgs(spec); err == nil {
		t.Error("unknown accel accepted")
	}
	spec.Accel, spec.MemoryMB = "tcg", 0
	if _, err := BuildArgs(spec); err == nil {
		t.Error("zero memory accepted")
	}
}

func TestCandidateAccels(t *testing.T) {
	x86 := Profile{Name: ProfileX86_64}
	arm := Profile{Name: ProfileARM64}
	cases := []struct {
		goos, arch string
		p          Profile
		cfg        string
		kvm        bool
		want       string
	}{
		{"windows", "amd64", x86, "auto", false, "whpx,tcg"},
		{"windows", "amd64", arm, "auto", false, "tcg"},
		{"linux", "amd64", x86, "auto", true, "kvm,tcg"},
		{"linux", "amd64", x86, "auto", false, "tcg"},
		{"darwin", "arm64", x86, "auto", false, "tcg"},
		{"darwin", "arm64", arm, "auto", false, "hvf,tcg"},
		{"windows", "amd64", x86, "tcg", false, "tcg"},
		{"windows", "amd64", x86, "whpx", false, "whpx,tcg"},
	}
	for _, c := range cases {
		if got := strings.Join(candidateAccels(c.goos, c.arch, c.p, c.cfg, c.kvm), ","); got != c.want {
			t.Errorf("%s/%s %s cfg=%s: %s, want %s", c.goos, c.arch, c.p.Name, c.cfg, got, c.want)
		}
	}
}

func TestClassifyQEMUFailure(t *testing.T) {
	cases := map[string]string{
		"qemu-system-x86_64.exe: WHPX: No accelerator found, hr=00000000":                       CodeAccelUnavailable,
		"Could not access KVM kernel module: Permission denied\nqemu: failed to initialize kvm": CodeAccelUnavailable,
		"qemu: cannot set up guest memory 'pc.ram': Cannot allocate memory":                     CodeOutOfMemory,
		"could not open disk image data.qcow2: Could not open 'x': No such file or directory":   CodeDiskError,
		"Failed to get \"write\" lock\nIs another process using the image [data.qcow2]?":        CodeDiskError,
		"something unexpected": CodeQEMUFailed,
	}
	for stderr, code := range cases {
		e := classifyQEMUFailure(stderr, errors.New("exit status 1"), "whpx")
		if e.Code != code {
			t.Errorf("%q → %s, want %s", stderr, e.Code, code)
		}
		if e.Title == "" || strings.Contains(strings.ToLower(e.Error()), "unknown error") {
			t.Errorf("%q: unhelpful message %q", stderr, e.Error())
		}
	}
	generic := classifyQEMUFailure("x", nil, "tcg").Error()
	for _, want := range []string{"Android sandbox could not start.", "Possible causes:", "Hardware virtualization is disabled.", "See Details"} {
		if !strings.Contains(generic, want) {
			t.Errorf("generic message lacks %q:\n%s", want, generic)
		}
	}
}

func TestParseSnapshotList(t *testing.T) {
	out := "List of snapshots present on all disks:\r\nID        TAG               VM SIZE                DATE     VM CLOCK     ICOUNT\r\n--        boot              512 MiB 2026-09-28 13:01:02  00:05:12.123\r\n--        my snap           1.2 GiB 2026-09-28 14:00:00  00:10:00.000\r\n"
	s := parseSnapshotList(out)
	if len(s) != 2 || s[0].Tag != "boot" || s[0].VMSize != "512 MiB" || s[0].Created.Hour() != 13 || s[1].Tag != "my snap" || s[1].VMSize != "1.2 GiB" {
		t.Fatalf("%+v", s)
	}
	if len(parseSnapshotList("There is no snapshot available.")) != 0 {
		t.Fatal("empty list")
	}
}

func TestProfileLoadAndValidate(t *testing.T) {
	root := t.TempDir()
	p := testProfile(t)
	dir := filepath.Join(root, "x86_64")
	os.MkdirAll(dir, 0o755)
	for _, f := range []string{"kernel", "initrd.img", "android.iso"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	b, _ := json.Marshal(p)
	os.WriteFile(filepath.Join(dir, "runtime.json"), b, 0o644)
	ps, err := LoadProfiles(root, "")
	if err != nil {
		t.Fatal(err)
	}
	got := ps[ProfileX86_64]
	var mf *MissingFileError
	if err := got.Validate(); !errors.As(err, &mf) || len(mf.Files) != 1 || mf.Files[0] != "data-template.qcow2" {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(err2s(got.Validate()), "Reinstall") {
		t.Error("missing-file error must tell the user what to do")
	}
	if _, err := LoadProfiles(t.TempDir(), ""); err == nil {
		t.Fatal("empty runtime dir accepted")
	}
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDataDiskLifecycle(t *testing.T) {
	p := testProfile(t)
	os.WriteFile(p.Path(p.DataTemplate), []byte("template"), 0o644)
	sandbox := t.TempDir()
	path, err := PrepareDataDisk(sandbox, p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("used"), 0o600)
	if again, _ := PrepareDataDisk(sandbox, p); again != path {
		t.Fatal("path changed")
	}
	if b, _ := os.ReadFile(path); string(b) != "used" {
		t.Fatal("existing disk must be kept")
	}
	if _, err := ResetDataDisk(sandbox, p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "template" {
		t.Fatal("reset must restore the template")
	}
}

// fakeQEMU speaks QMP on conn.
func fakeQEMU(t *testing.T, conn net.Conn) {
	go func() {
		defer conn.Close()
		fmt.Fprintln(conn, `{"QMP": {"version": {"qemu": {"major": 11}}, "capabilities": []}}`)
		r := bufio.NewReader(conn)
		jobs := 0
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var m struct {
				Execute   string         `json:"execute"`
				ID        string         `json:"id"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(line, &m)
			reply := func(ret string) { fmt.Fprintf(conn, `{"return": %s, "id": %q}`+"\n", ret, m.ID) }
			switch m.Execute {
			case "qmp_capabilities":
				fmt.Fprintln(conn, `{"return": {}}`)
			case "query-status":
				fmt.Fprintln(conn, `{"timestamp": {"seconds": 1, "microseconds": 2}, "event": "RESET", "data": {"guest": true}}`)
				reply(`{"status": "running", "running": true}`)
			case "snapshot-save":
				jobs++
				reply(`{}`)
			case "query-jobs":
				reply(fmt.Sprintf(`[{"id": "x", "status": "running"}, {"id": %q, "status": "concluded"}]`, "keep"))
			case "query-vnc":
				reply(`{"enabled": true, "host": "127.0.0.1", "service": "5912", "family": "ipv4"}`)
			case "human-monitor-command":
				reply(`"There is no snapshot available.\r\n"`)
			case "boom":
				fmt.Fprintf(conn, `{"error": {"class": "GenericError", "desc": "nope"}, "id": %q}`+"\n", m.ID)
			case "quit":
				reply(`{}`)
				return
			default:
				reply(`{}`)
			}
		}
	}()
}

func TestQMPClient(t *testing.T) {
	a, b := net.Pipe()
	fakeQEMU(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q, err := NewQMP(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	st, err := q.Status(ctx)
	if err != nil || st != "running" {
		t.Fatalf("status %q %v", st, err)
	}
	select {
	case ev := <-q.Events():
		if ev.Name != "RESET" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered")
	}
	var qe *QMPError
	if err := q.Execute(ctx, "boom", nil, nil); !errors.As(err, &qe) || qe.Desc != "nope" || qe.Command != "boom" {
		t.Fatalf("error: %v", err)
	}
	if snaps, err := q.ListSnapshots(ctx); err != nil || len(snaps) != 0 {
		t.Fatalf("snapshots %v %v", snaps, err)
	}
	// Concurrent commands are correlated by id.
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() { _, err := q.Status(ctx); errs <- err }()
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	q.Execute(ctx, "quit", nil, nil)
	<-q.Done()
	if err := q.Execute(ctx, "query-status", nil, nil); !errors.Is(err, ErrQMPClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestQMPRejectsNonQMPPeer(t *testing.T) {
	a, b := net.Pipe()
	go func() { io.WriteString(b, "SSH-2.0-OpenSSH\n"); b.Close() }()
	if _, err := NewQMP(context.Background(), a); err == nil {
		t.Fatal("non-QMP peer accepted")
	}
}

// fakeShell emulates a tty shell on a serial line: it echoes input (like a
// terminal), expands $((n)) and prints command output.
func fakeShell(conn net.Conn, handle func(cmd string) (string, int)) {
	arith := regexp.MustCompile(`\$\(\((\d+)\)\)`)
	go func() {
		r := bufio.NewReader(conn)
		io.WriteString(conn, "[   12.3] random kernel noise\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			io.WriteString(conn, line+"\r\n") // terminal echo (contains unexpanded markers)
			for _, part := range strings.Split(line, "; ") {
				part = arith.ReplaceAllString(part, "$1")
				switch {
				case strings.HasPrefix(part, "echo @@E"):
					io.WriteString(conn, "\x1b[0m"+strings.Replace(strings.TrimPrefix(part, "echo "), "$?", "0", 1)+"\r\n")
				case strings.HasPrefix(part, "echo "):
					io.WriteString(conn, strings.TrimPrefix(part, "echo ")+"\r\n")
				default:
					out, _ := handle(part)
					io.WriteString(conn, out)
				}
			}
			io.WriteString(conn, "console:/ # ")
		}
	}()
}

func TestConsoleRun(t *testing.T) {
	a, b := net.Pipe()
	fakeShell(b, func(cmd string) (string, int) {
		if cmd == "getprop sys.boot_completed" {
			return "1\r\n", 0
		}
		return "", 0
	})
	c := NewConsole(a)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitShell(ctx); err != nil {
		t.Fatal(err)
	}
	out, code, err := c.Run(ctx, "getprop sys.boot_completed")
	if err != nil || code != 0 || out != "1" {
		t.Fatalf("out=%q code=%d err=%v", out, code, err)
	}
	if _, _, err := c.Run(ctx, "two\nlines"); err == nil {
		t.Fatal("multi-line command accepted")
	}
	b.Close()
	if _, _, err := c.Run(ctx, "echo x"); err == nil {
		t.Fatal("closed console must fail")
	}
}
