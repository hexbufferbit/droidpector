#!/usr/bin/env bash
# Prepares the redistributable runtime bundle (build/runtime):
#
#   runtime/qemu/                 QEMU for Windows x64 (unmodified upstream binaries,
#                                 pruned to the executables we use + their DLLs)
#   runtime/android/x86_64/       BlissOS 16 (Android 13) ISO, kernel, initrd,
#                                 pre-formatted ext4 data template, runtime.json
#   runtime/licenses/             license texts and the source-code offer
#
# Every download is pinned by SHA-256. Requires: bash, curl, 7z (p7zip), qemu-img,
# mkfs.ext4 (e2fsprogs), erofs-utils, squashfs-tools, xorriso, Go.
# Run on Linux CI (or macOS with Homebrew tools).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${1:-$ROOT/build/runtime}"
CACHE="$ROOT/.cache/downloads"

BLISS_URL="https://sourceforge.net/projects/blissos-x86/files/Official/BlissOS16/FOSS/Generic/Bliss-v16.9.7-x86_64-OFFICIAL-foss-20241011.iso/download"
BLISS_SHA256="735cb962ec6bd92b62eb82a812831a38d79a0dfdf12b7973d2d0f7ab001ba68e"
BLISS_SOURCE="https://github.com/BlissRoms-x86 (BlissOS 16.9.7, tag 20241011)"
QEMU_URL="https://qemu.weilnetz.de/w64/qemu-w64-setup-20260811.exe"
QEMU_SHA256="f98a8aeb5f7faea9765b6dee28316c266cd179d80354a2fed8e50176f9a2e59f"
QEMU_SOURCE="https://qemu.weilnetz.de/w64/ (build 20260811; source: https://gitlab.com/qemu-project/qemu and https://qemu.weilnetz.de/)"

SEVENZIP="${SEVENZIP:-$(command -v 7z || command -v 7zz)}"
MKFS="${MKFS:-$(command -v mkfs.ext4 || echo /opt/homebrew/opt/e2fsprogs/sbin/mkfs.ext4)}"

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

fetch() { # url sha256 dest
  local url="$1" want="$2" dest="$3"
  if [ -f "$dest" ] && [ "$(sha256 "$dest")" = "$want" ]; then return; fi
  echo "downloading $(basename "$dest")..."
  curl -fL --retry 5 --retry-delay 5 -o "$dest.part" "$url"
  local got; got="$(sha256 "$dest.part")"
  if [ "$got" != "$want" ]; then
    echo "checksum mismatch for $dest: got $got want $want" >&2
    exit 1
  fi
  mv "$dest.part" "$dest"
}

mkdir -p "$CACHE" "$OUT"
fetch "$BLISS_URL" "$BLISS_SHA256" "$CACHE/bliss16-foss.iso"
fetch "$QEMU_URL" "$QEMU_SHA256" "$CACHE/qemu-w64-setup.exe"

# --- Android x86_64 runtime -------------------------------------------------
# BlissOS ships its system image as system.efs (EROFS, lz4) = 2.2 GB, which is
# above the ~2 GB payload limit of single-file installers. We repack the SAME
# ext4 system image into an xz squashfs (≈1.6 GB) inside a small ISO that the
# BlissOS initrd finds via SRC/ROOT. The repack is verified to be lossless.
A="$OUT/android/x86_64"
W="$CACHE/android-work"
rm -rf "$A" "$W" && mkdir -p "$A" "$W/iso"
"$SEVENZIP" e -y -o"$A" "$CACHE/bliss16-foss.iso" kernel initrd.img >/dev/null
"$SEVENZIP" e -y -o"$W" "$CACHE/bliss16-foss.iso" system.efs >/dev/null
fsck.erofs --extract="$W/efs" "$W/system.efs" >/dev/null
ORIG_SHA="$(sha256 "$W/efs/system.img")"
export SOURCE_DATE_EPOCH=1728604800   # BlissOS 16.9.7 release date: reproducible output
# (SOURCE_DATE_EPOCH alone sets every timestamp; mksquashfs refuses it combined with -mkfs-time.)
mksquashfs "$W/efs/system.img" "$W/iso/system.sfs" -comp xz -b 1M -Xdict-size 100% -noappend -no-progress -quiet
REPACK_SHA="$(unsquashfs -cat "$W/iso/system.sfs" system.img | { if command -v sha256sum >/dev/null; then sha256sum; else shasum -a 256; fi; } | cut -d' ' -f1)"
if [ "$ORIG_SHA" != "$REPACK_SHA" ]; then
  echo "repacked system image differs from the original ($ORIG_SHA vs $REPACK_SHA)" >&2
  exit 1
fi
xorriso -as mkisofs -iso-level 3 -V ANDROID_SYSTEM -R -o "$A/system.iso" "$W/iso" >/dev/null 2>&1
rm -rf "$W"
# 8 GiB sparse ext4 for /data, stored as a tiny qcow2 (copied per sandbox).
TMPRAW="$(mktemp -d)/data.raw"
truncate -s 8G "$TMPRAW"
"$MKFS" -q -F -L data -O ^metadata_csum_seed,^orphan_file "$TMPRAW" 2>/dev/null || "$MKFS" -q -F -L data "$TMPRAW"
qemu-img convert -O qcow2 "$TMPRAW" "$A/data-template.qcow2"
rm -f "$TMPRAW"
SYSTEM_SHA="$(sha256 "$A/system.iso")"
sed -e "s/@SYSTEM_ISO_SHA256@/$SYSTEM_SHA/" -e "s/@SYSTEM_IMG_SHA256@/$ORIG_SHA/" \
    "$ROOT/src/installer/runtime/x86_64/runtime.json" > "$A/runtime.json"
echo "system image repacked losslessly: system.img sha256 $ORIG_SHA → system.iso $(du -h "$A/system.iso" | cut -f1)"

# --- QEMU for Windows ---------------------------------------------------------
Q="$OUT/qemu"
rm -rf "$Q" "$CACHE/qemu-x"
"$SEVENZIP" x -y -o"$CACHE/qemu-x" "$CACHE/qemu-w64-setup.exe" >/dev/null
(cd "$ROOT" && go run ./tools/runtime/qemuprune -src "$CACHE/qemu-x" -dst "$Q" \
  -exe qemu-system-x86_64.exe -exe qemu-system-aarch64.exe -exe qemu-img.exe)

# --- licenses & source offer ----------------------------------------------------
L="$OUT/licenses"
mkdir -p "$L"
cp "$Q/COPYING" "$L/QEMU-COPYING.txt"
[ -f "$Q/COPYING.LIB" ] && cp "$Q/COPYING.LIB" "$L/QEMU-COPYING.LIB.txt"
cp "$ROOT/src/installer/NOTICE.txt" "$L/NOTICE.txt"
cat > "$L/SOURCES.txt" <<EOF
droidpector redistributes the following third-party components unmodified.
Their complete corresponding source code is available from the locations
below; on request we provide it on a physical medium for at least three years
(GPL-2.0 §3(b)). Contact: see README.md.

QEMU (GPL-2.0-only; some parts LGPL/BSD/MIT)
  Binary: $QEMU_URL
  SHA-256: $QEMU_SHA256
  Source: $QEMU_SOURCE

BlissOS 16.9.7 FOSS x86_64 (Apache-2.0; Linux kernel GPL-2.0; other components under their own licenses)
  Binary: $BLISS_URL
  SHA-256: $BLISS_SHA256
  Source: $BLISS_SOURCE
  Repackaging: the system image is stored losslessly in an xz squashfs (byte-identical system.img).
  Note: this image includes libndk_translation (ARM-to-x86 translation) as shipped by the BlissOS project.
EOF
echo "runtime prepared in $OUT"
du -sh "$OUT"/* 2>/dev/null || true
