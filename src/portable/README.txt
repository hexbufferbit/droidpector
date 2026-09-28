droidpector (portable)
======================

Run an Android app in an isolated sandbox and inspect its network traffic.

Start
-----
Double-click droidpector.exe. Nothing is installed: the program, the Android
runtime and all your data (sandbox, captured sessions, logs, settings in the
"data" folder) stay inside this folder. To remove droidpector, delete the
folder. To move it, move the folder (keep it on a local drive with ~10 GB free).

Requirements
------------
- Windows 10 or 11, 64-bit, 8 GB RAM (16 GB recommended).
- Microsoft Edge WebView2 Runtime (already part of Windows 11 and updated
  Windows 10). If droidpector reports it missing, install it from
  https://go.microsoft.com/fwlink/p/?LinkId=2124703
- For full speed: hardware virtualization enabled in the BIOS/UEFI and the
  Windows feature "Windows Hypervisor Platform" turned on:
  Start > "Turn Windows features on or off" > Windows Hypervisor Platform,
  then restart. Without it Android still runs, but slowly (software emulation).

First start
-----------
The first start boots Android completely (a few minutes). When you stop the
sandbox, its state is saved; the next start takes seconds.

Do not extract into "C:\Program Files" (not writable); use e.g.
C:\Tools\droidpector or your Desktop.

Licenses: runtime\licenses (NOTICE.txt, SOURCES.txt).
Use droidpector only on applications you are authorized to analyse.
