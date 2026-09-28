# VM — Android sandbox runtime

## Runtime profiles

A runtime profile is a folder `runtime/android/<name>/` with a manifest
`runtime.json` (loaded by `vm.LoadProfiles`), so images can be updated without
code changes:

| field | meaning |
|---|---|
| `qemu`, `machine` | emulator binary and machine type (`qemu-system-x86_64`, `q35`) |
| `kernel`, `initrd`, `iso` | direct kernel boot + read-only system image (`system.iso`: xz squashfs of the unmodified BlissOS system image, CD-ROM) |
| `dataTemplate` | pre-formatted sparse ext4 (8 GiB virtual, ~6 MB qcow2) copied to the sandbox on first use |
| `cmdline` | kernel command line |
| `abis`, `translation` | CPU ABIs the image runs; whether an ARM translator is present |
| `sha256` | integrity of large files |

### x86_64 (default): BlissOS 16.9.7 FOSS — Android 13, API 33

Kernel command line: `root=/dev/ram0 SRC= DATA=vda ROOT=/dev/sr0 nomodeset
HWACCEL=0 console=ttyS0 quiet androidboot.enable_console=1
androidboot.selinux=permissive SETUPWIZARD=0`

- `ROOT=/dev/sr0` makes the initrd take the system image from the CD-ROM;
  `DATA=vda` mounts the first virtio disk (the sandbox disk) as `/data`.
- `androidboot.enable_console=1` + `console=ttyS0` give a root console on the
  serial port (bootstrap/diagnostic channel).
- `nomodeset HWACCEL=0`: software rendering (no host GPU dependency).
- The image is `userdebug` (`ro.adb.secure=0`, adbd listens on TCP 5555 and
  can restart as root) and includes `libndk_translation`, so arm64-v8a and
  armeabi-v7a APKs run on the x86_64 VM.

### arm64 (fallback profile)

ADR-004 reserves an `arm64` profile (`qemu-system-aarch64`, `virt`, TCG) for
ARM-only apps that fail under translation. The adapter, argument builder and
accelerator selection support it; an Android arm64 image is not bundled yet
(see Known issues in the milestone report).

## QEMU command line (x86_64)

Built by `vm.BuildArgs` (pure function, unit tested):

```
-nodefaults -no-user-config -machine q35
-accel whpx,kernel-irqchip=off | kvm | hvf | tcg,thread=multi,tb-size=512   -cpu max|host
-smp N -m M -rtc base=utc,clock=host
-kernel … -initrd … -append …
-blockdev file(iso, read-only) → raw  -device ide-cd
-blockdev file(data.qcow2) → qcow2 node "data"  -device virtio-blk-pci
-netdev socket,connect=127.0.0.1:<gateway>  -device virtio-net-pci       ← the only NIC
-vga std  -device qemu-xhci + usb-tablet (absolute pointer) + usb-kbd
-display none -vnc 127.0.0.1:<n>,password=on                               ← per-boot password via QMP
-chardev socket,server=off → QMP (control)     -chardev socket,server=off → serial console
-device pvpanic  [-loadvm quickstart]
```

All three control channels connect **to listeners owned by the core** on
127.0.0.1 (QEMU is the client), so no QEMU control port is ever listening.
Paths are escaped for QEMU option syntax (`,` → `,,`).

## Lifecycle (core.Sandbox)

```
stopped → preparing → starting → booting → provisioning → ready (Network Capture Active)
                       │            │           │
                       └── error ◄──┴───────────┘      ready → installing → launching → ready
ready ──unexpected QEMU exit / guest panic──► recovering → (restart) → ready
```

1. **Prepare**: data disk (copy of template), session, profile selection.
2. **Start**: `vm.Start` tries accelerators in order (Windows: WHPX → TCG;
   Linux: KVM → TCG; macOS: HVF for same-arch → TCG), classifying QEMU stderr
   into actionable errors (`vm.StartError`: WHPX disabled, out of memory, disk
   locked, missing runtime files, CPU lacks SSE4.2 …). Falling back to TCG
   shows a warning explaining how to enable the Windows Hypervisor Platform.
3. **Display**: VNC connected immediately, so the user sees Android boot.
4. **Boot detection**: the console bootstrap waits for Android's shell
   (`getprop` works) and ensures `persist.adb.tcp.port=5555`; ADB connects
   through the sandbox network (`Stack.DialGuest`), restarts adbd as root
   (`root:` service), and waits for `sys.boot_completed=1` and a responsive
   package manager.
5. **Provisioning** (every boot/restore): clock sync (vital after snapshot
   restore), private DNS off (DNS stays observable), package verification off,
   stay-awake, keyguard dismissed, fresh per-boot CA installed via tmpfs
   overlay over `/system/etc/security/cacerts` (and the Conscrypt APEX store
   on Android 14+).
6. **Quick start** (like the Android emulator's quick boot): a graceful
   Stop/Restart saves the complete VM state as the `quickstart` internal
   snapshot; the next start resumes from it in seconds with every installed
   app and file intact, then the snapshot is consumed (the running VM
   diverges from it). Crashed or failed VMs are never saved and cold boot
   from their current disk, so no data is rolled back. The state is keyed by
   memory size and image; if it cannot be loaded it is discarded and the VM
   cold boots. Provisioning also selects a default launcher so Android never
   stops at "Select a Home app".

### Crash detection and recovery

`vm.Machine.watch` turns process exit and QMP `GUEST_PANICKED` into events.
An unexpected exit triggers `Sandbox.recover`: cleanup (stop QEMU, drop ADB
and display, keep the session) → cold boot with the same profile from the
current disk (installed apps and data preserved) → provision → relaunch the
app that was running. The budget is 3 recoveries per 10 minutes
(`shouldRecover`); beyond it the UI shows an error with a diagnostic bundle
option. QEMU runs in a Windows Job Object (kill-on-close) / PDEATHSIG on
Linux, so it can never outlive the application.

### Snapshots and reset

User snapshots (`[A-Za-z0-9_-]{1,40}`) use QMP `snapshot-save/load/delete`
jobs on the data disk node (RAM + devices + disk). After a restore the core
reconnects ADB and re-provisions (clock, CA). **Reset** stops the VM, deletes
the sandbox disk (apps, data, snapshots, quick-start) and cold boots from the
template.

## Display and input

`display.Client` (RFB 3.8, VNC auth with DES, Raw/CopyRect/DesktopSize,
32-bpp true colour mapped directly to RGBA) keeps the framebuffer;
`display.Streamer` coalesces damage at ≤30 fps and sends JPEG rectangles over
the authenticated WebSocket; slow clients are resynchronized with a full frame
instead of blocking. Pointer (absolute via usb-tablet, wheel as buttons 4/5)
and X11 keysyms flow back. Paste uses `input text` via ADB (printable ASCII).
