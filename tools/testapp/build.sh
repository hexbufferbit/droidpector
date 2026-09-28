#!/usr/bin/env bash
# Builds the TestApp APK without the Android SDK:
#   javac (against minimal API stubs) → d8 (R8, Apache-2.0, from Google Maven)
#   → tools/apkbuild (binary manifest, resources, zip, APK Signature Scheme v2).
# Requirements: JDK 11+ (javac, java), Go, curl. Output: build/testapp/TestApp.apk
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/build/testapp"
R8_VERSION="${R8_VERSION:-8.5.35}"
R8_SHA256="${R8_SHA256:-}"
CACHE="$ROOT/.cache/tools"
JAVAC="${JAVAC:-javac}"
JAVA="${JAVA:-java}"
mkdir -p "$OUT/stubs" "$OUT/classes" "$OUT/dex" "$CACHE"

R8_JAR="$CACHE/r8-$R8_VERSION.jar"
if [ ! -f "$R8_JAR" ]; then
  curl -fsSL -o "$R8_JAR.tmp" "https://maven.google.com/com/android/tools/r8/$R8_VERSION/r8-$R8_VERSION.jar"
  mv "$R8_JAR.tmp" "$R8_JAR"
fi
if [ -n "$R8_SHA256" ]; then
  echo "$R8_SHA256  $R8_JAR" | sha256sum -c - >/dev/null
fi

cd "$ROOT/tools/testapp"
"$JAVAC" --release 8 -nowarn -d "$OUT/stubs" $(find stubs -name '*.java')
(cd "$OUT/stubs" && jar cf ../android-stubs.jar .)
"$JAVAC" --release 8 -nowarn -cp "$OUT/android-stubs.jar" -d "$OUT/classes" $(find src -name '*.java')
rm -f "$OUT/dex/"*.dex
"$JAVA" -cp "$R8_JAR" com.android.tools.r8.D8 --release --min-api 26 --no-desugaring \
  --lib "$OUT/android-stubs.jar" --output "$OUT/dex" $(find "$OUT/classes" -name '*.class')

KEY="$ROOT/tools/testapp/testapp-debug-key.pem"
KEYARGS=(-key "$KEY")
[ -f "$KEY" ] || KEYARGS=(-save-key "$KEY")
cd "$ROOT"
go run ./tools/apkbuild -manifest tools/testapp/AndroidManifest.xml -dex "$OUT/dex/classes.dex" \
  -string app_name="droidpector TestApp" "${KEYARGS[@]}" -out "$OUT/TestApp.apk"
echo "built $OUT/TestApp.apk"
