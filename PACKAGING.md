# Packaging

## Deliverables

| File | Content |
|---|---|
| `droidpector-portable-x64.zip` | **primary**: portable folder (≈1.8 GB). Extract anywhere writable and run `droidpector.exe`; no installation, no registry, no AppData |
| `droidpector.exe` | Release executable (Go, UI embedded, icon + version resources, no runtime DLLs) |
| `droidpector-Setup-x64.exe` | optional NSIS installer (`make installer`) |

## Portable layout (`make portable`)

```
droidpector/
  droidpector.exe
  README.txt
  runtime/qemu/ …            QEMU for Windows (pruned)
  runtime/android/x86_64/    kernel, initrd.img, system.iso, data-template.qcow2, runtime.json
  runtime/licenses/          NOTICE.txt, SOURCES.txt, QEMU-COPYING.txt
  data/                      created on first run: sandbox disk, sessions, logs, config, keys, WebView2 profile
```

When `droidpector.exe` sits next to a `runtime` folder, all writable state
goes to `data/` beside it (`platform.portableDataDir`); if that folder is not
writable it falls back to `%LOCALAPPDATA%\droidpector`. Uninstall = delete the
folder. The portable build cannot enable Windows Hypervisor Platform or
install WebView2; the app explains both when needed (README.txt too).

## Installer behaviour (optional installer)

## Installer behaviour

1. Refuses non-x64 or pre-Windows 10 systems.
2. Closes a running instance.
3. Installs to `%ProgramFiles%\droidpector`: the exe, pruned QEMU,
   kernel/initrd/data template/runtime manifest, licenses.
4. Installs the Microsoft Edge WebView2 Runtime (Evergreen bootstrapper) if
   it is missing.
5. **Android system image** (`system.iso`, 1.64 GB) is embedded in the
   installer. BlissOS's 2.2 GB EROFS image is above NSIS's 2 GB payload limit,
   so the build repacks the identical ext4 system image into an xz squashfs
   (ADR-012); `prepare.sh` verifies byte-identity. No download is needed.
6. Optional (checked by default): enables **Windows Hypervisor Platform**
   (`dism /enable-feature HypervisorPlatform`, restart requested) for
   hardware-accelerated Android.
7. Start-menu shortcut, optional desktop shortcut, Add/Remove Programs entry
   (with quiet uninstall string and size).

No environment variables are set; all paths are derived at runtime (install
dir from the executable, data from `%LOCALAPPDATA%`).

## Uninstall

Removes the program folder, shortcuts and registry entry. The user is asked
whether to delete their sandbox and sessions (`%LOCALAPPDATA%\droidpector`);
silent uninstall keeps them unless `/REMOVEDATA` is given.

## Silent install

```
droidpector-Setup-x64.exe /S [/D=C:\Path]
"%ProgramFiles%\droidpector\Uninstall.exe" /S [/REMOVEDATA]
```

## Redistribution

- QEMU: GPL-2.0, unmodified upstream Windows build, separate process; the
  source offer is in `runtime\licenses\SOURCES.txt`.
- Android image: BlissOS 16.9.7 FOSS, hash-pinned download at build time,
  content unmodified (only re-compressed). It contains the
  `libndk_translation` ARM translation library as distributed by BlissOS;
  redistributing it inside our installer is a legal decision to confirm
  before public release.
- WebView2 bootstrapper: Microsoft redistributable.

## Code signing

Not configured. For public releases sign `droidpector.exe` (`make sign
SIGN_PFX=… SIGN_PASS=…`) — unsigned executables trigger SmartScreen and
antivirus warnings.
