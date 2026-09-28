package vm

import (
	"fmt"
	"strings"
)

// LaunchSpec is everything needed to build a QEMU command line.
type LaunchSpec struct {
	Profile    Profile
	Accel      string // whpx, kvm, hvf, tcg
	MemoryMB   int
	CPUs       int
	DataDisk   string // writable qcow2 (holds /data and internal snapshots)
	QMPAddr    string // host:port the core listens on for QMP
	SerialAddr string // host:port the core listens on for the console
	NetAddr    string // host:port of the gateway link listener
	VNCPort    int    // loopback port QEMU listens on for VNC
	SerialLog  string // file mirroring the console output
	LoadVM     string // snapshot tag to restore at start ("" = cold boot)
	GuestMAC   string
}

// DataNode is the block node name of the data disk (snapshot target).
const DataNode = "data"

// escapeOpt escapes a value for QEMU's comma-separated option syntax.
func escapeOpt(s string) string { return strings.ReplaceAll(s, ",", ",,") }

func hostPort(addr string) (string, string) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, ""
	}
	return addr[:i], addr[i+1:]
}

// BuildArgs produces the QEMU argument list. It is a pure function so the
// exact command line is unit tested.
func BuildArgs(s LaunchSpec) ([]string, error) {
	p := s.Profile
	if s.MemoryMB <= 0 || s.CPUs <= 0 {
		return nil, fmt.Errorf("invalid VM resources: %d MiB, %d CPUs", s.MemoryMB, s.CPUs)
	}
	mac := s.GuestMAC
	if mac == "" {
		mac = "52:54:00:12:34:56"
	}
	args := []string{
		"-name", "droidpector-" + p.Name,
		"-nodefaults",
		"-no-user-config",
		"-machine", p.Machine,
	}
	switch s.Accel {
	case "whpx":
		args = append(args, "-accel", "whpx,kernel-irqchip=off", "-cpu", cpuFor(p))
	case "kvm", "hvf":
		args = append(args, "-accel", s.Accel, "-cpu", "host")
	case "tcg", "":
		args = append(args, "-accel", "tcg,thread=multi,tb-size=512", "-cpu", cpuFor(p))
	default:
		return nil, fmt.Errorf("unknown accelerator %q", s.Accel)
	}
	args = append(args,
		"-smp", fmt.Sprint(s.CPUs),
		"-m", fmt.Sprintf("%dM", s.MemoryMB),
		"-rtc", "base=utc,clock=host",
	)
	if p.Kernel != "" {
		args = append(args, "-kernel", p.Path(p.Kernel))
	}
	if p.Initrd != "" {
		args = append(args, "-initrd", p.Path(p.Initrd))
	}
	if p.Cmdline != "" {
		args = append(args, "-append", p.Cmdline)
	}
	if p.ISO != "" {
		args = append(args,
			"-blockdev", "driver=file,filename="+escapeOpt(p.Path(p.ISO))+",node-name=cd-file,read-only=on",
			"-blockdev", "driver=raw,file=cd-file,node-name=cd,read-only=on",
		)
		if p.Name == ProfileARM64 {
			args = append(args, "-device", "virtio-blk-pci,drive=cd")
		} else {
			args = append(args, "-device", "ide-cd,drive=cd,bus=ide.1")
		}
	}
	if s.DataDisk == "" {
		return nil, fmt.Errorf("no data disk")
	}
	args = append(args,
		"-blockdev", "driver=file,filename="+escapeOpt(s.DataDisk)+",node-name=data-file,discard=unmap",
		"-blockdev", "driver=qcow2,file=data-file,node-name="+DataNode+",discard=unmap",
		"-device", "virtio-blk-pci,drive="+DataNode,
	)
	// Networking: the ONLY NIC, connected to the in-process gateway.
	args = append(args,
		"-netdev", "socket,id=net0,connect="+s.NetAddr,
		"-device", "virtio-net-pci,netdev=net0,mac="+mac,
	)
	// Display + input.
	if p.Name == ProfileARM64 {
		args = append(args, "-device", "ramfb")
	} else {
		args = append(args, "-vga", "std")
	}
	args = append(args,
		"-device", "qemu-xhci,id=xhci", "-device", "usb-tablet,bus=xhci.0", "-device", "usb-kbd,bus=xhci.0",
		"-display", "none",
		"-vnc", fmt.Sprintf("127.0.0.1:%d,password=on", s.VNCPort-5900),
	)
	// Control and console channels: QEMU connects to listeners owned by the core.
	qh, qp := hostPort(s.QMPAddr)
	sh, sp := hostPort(s.SerialAddr)
	serial := fmt.Sprintf("socket,id=con,host=%s,port=%s,server=off", sh, sp)
	if s.SerialLog != "" {
		serial += ",logfile=" + escapeOpt(s.SerialLog) + ",logappend=on"
	}
	args = append(args,
		"-chardev", fmt.Sprintf("socket,id=qmp,host=%s,port=%s,server=off", qh, qp),
		"-mon", "chardev=qmp,mode=control",
		"-chardev", serial,
		"-serial", "chardev:con",
	)
	if p.Name != ProfileARM64 {
		args = append(args, "-device", "pvpanic")
	}
	if p.QEMUDir != "" {
		args = append(args, "-L", p.QEMUDir+"/share")
	}
	if s.LoadVM != "" {
		args = append(args, "-loadvm", s.LoadVM)
	}
	return args, nil
}

func cpuFor(p Profile) string {
	if p.Name == ProfileARM64 {
		return "max"
	}
	// Android x86_64 needs SSE4.2/POPCNT/SSSE3; "max" exposes every feature the
	// accelerator can emulate or pass through.
	return "max"
}
