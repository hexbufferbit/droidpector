package apkbuild_test

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/droidpector/apkinspector/src/android/apk"
	"github.com/droidpector/apkinspector/tools/apkbuild/apkbuild"
)

var (
	keyOnce sync.Once
	keyVal  *rsa.PrivateKey
)

// testKey returns an RSA key shared across tests (key generation is slow).
func testKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		keyVal = k
	})
	return keyVal
}

const manifestXML = `<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android"
    package="com.apkinspector.testapp" android:versionCode="42" android:versionName="1.2.3">
  <uses-sdk android:minSdkVersion="24" android:targetSdkVersion="33"/>
  <uses-permission android:name="android.permission.INTERNET"/>
  <application android:label="@string/app_name" android:extractNativeLibs="false">
    <activity android:name=".MainActivity" android:exported="true">
      <intent-filter>
        <action android:name="android.intent.action.MAIN"/>
        <category android:name="android.intent.category.LAUNCHER"/>
      </intent-filter>
    </activity>
  </application>
</manifest>`

func buildTestAPK(t *testing.T, extra ...apkbuild.File) []byte {
	t.Helper()
	signer, err := apkbuild.NewSelfSigned(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	files := append([]apkbuild.File{
		{Name: "classes.dex", Data: bytes.Repeat([]byte("dex\n035\x00"), 1000)},
		{Name: "assets/big.bin", Data: pseudoRandom(3<<20 + 123), Store: true},
	}, extra...)
	b, err := apkbuild.Build(apkbuild.Spec{
		ManifestXML: []byte(manifestXML),
		Strings:     []apkbuild.StringResource{{Name: "app_name", Values: []apkbuild.LocalizedValue{{Value: "Test App"}, {Locale: "de", Value: "Test-App"}}}},
		Files:       files,
		Signer:      signer,
		AXML:        apkbuild.AXMLOptions{UTF8: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pseudoRandom(n int) []byte {
	b := make([]byte, n)
	var x uint32 = 2463534242
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func TestBuildVerifiesAndInspects(t *testing.T) {
	b := buildTestAPK(t, apkbuild.File{Name: "lib/x86_64/libnative.so", Data: []byte("\x7fELF fake"), Store: true})
	if err := verifyV2(b); err != nil {
		t.Fatalf("independent v2 verification failed: %v", err)
	}
	info, err := apk.InspectReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Package != "com.apkinspector.testapp" || info.VersionCode != 42 || info.VersionName != "1.2.3" {
		t.Errorf("identity = %q %d %q", info.Package, info.VersionCode, info.VersionName)
	}
	if info.Label != "Test App" {
		t.Errorf("label = %q, want Test App (default config)", info.Label)
	}
	if info.LaunchableActivity != "com.apkinspector.testapp.MainActivity" {
		t.Errorf("launcher = %q", info.LaunchableActivity)
	}
	if info.MinSDK != 24 || info.TargetSDK != 33 {
		t.Errorf("sdk = %d/%d", info.MinSDK, info.TargetSDK)
	}
	if !info.Signature.V2 || info.Signature.V1 {
		t.Errorf("signature = %+v", info.Signature)
	}
	if info.ExtractNativeLibs == nil || *info.ExtractNativeLibs {
		t.Errorf("extractNativeLibs = %v", info.ExtractNativeLibs)
	}
	if !info.Installable() {
		t.Errorf("issues: %+v", info.Issues)
	}
	if d := info.RequiredRuntime(false); d.Runtime != apk.RuntimeX86_64 {
		t.Errorf("runtime = %+v", d)
	}
}

func TestAlignment(t *testing.T) {
	b := buildTestAPK(t, apkbuild.File{Name: "lib/arm64-v8a/libx.so", Data: []byte("elf"), Store: true})
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Method != zip.Store {
			continue
		}
		off, err := f.DataOffset()
		if err != nil {
			t.Fatal(err)
		}
		align := int64(4)
		if strings.HasSuffix(f.Name, ".so") {
			align = 4096
		}
		if off%align != 0 {
			t.Errorf("%s data offset %d not %d-aligned", f.Name, off, align)
		}
		if f.Flags&0x8 != 0 {
			t.Errorf("%s uses a data descriptor", f.Name)
		}
	}
	if zr.File[0].Name != "AndroidManifest.xml" {
		t.Errorf("first entry = %s", zr.File[0].Name)
	}
}

func TestDeterministic(t *testing.T) {
	a, b := buildTestAPK(t), buildTestAPK(t)
	if !bytes.Equal(a, b) {
		t.Error("two builds with the same inputs differ")
	}
}

func TestTamperDetected(t *testing.T) {
	b := buildTestAPK(t)
	b[100] ^= 0xFF // inside the zip entries section
	if err := verifyV2(b); err == nil {
		t.Fatal("tampered APK verified")
	}
}

func TestUnsignedAndErrors(t *testing.T) {
	b, err := apkbuild.Build(apkbuild.Spec{ManifestXML: []byte(`<manifest package="a.b"/>`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyV2(b); err == nil {
		t.Error("unsigned APK verified")
	}
	if _, err := apkbuild.Build(apkbuild.Spec{ManifestXML: []byte(`<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="a"><application android:label="@string/missing"/></manifest>`)}); err == nil {
		t.Error("unknown string reference accepted")
	}
	if _, err := apkbuild.BuildZip([]apkbuild.File{{Name: "../evil"}}); err == nil {
		t.Error("path traversal name accepted")
	}
	if _, err := apkbuild.SignV2([]byte("nope"), &apkbuild.Signer{}); err == nil {
		t.Error("SignV2 accepted empty signer")
	}
}

func TestKeyPEMRoundTrip(t *testing.T) {
	k := testKey(t)
	p, err := apkbuild.EncodeKeyPEM(k)
	if err != nil {
		t.Fatal(err)
	}
	k2, cert, err := apkbuild.ParseKeyPEM(p)
	if err != nil || cert != nil || !k2.Equal(k) {
		t.Fatalf("round trip: %v %v", err, cert)
	}
	if _, _, err := apkbuild.ParseKeyPEM([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the command")
	}
	dir := t.TempDir()
	mf := filepath.Join(dir, "AndroidManifest.xml")
	dex := filepath.Join(dir, "classes.dex")
	out := filepath.Join(dir, "app.apk")
	must(t, os.WriteFile(mf, []byte(manifestXML), 0o644))
	must(t, os.WriteFile(dex, []byte("dex\n035\x00"), 0o644))
	cmd := exec.Command("go", "run", "..", "-manifest", mf, "-dex", dex, "-string", "app_name=CLI App", "-out", out, "-save-key", filepath.Join(dir, "k.pem"))
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apkbuild: %v\n%s", err, o)
	}
	b, err := os.ReadFile(out)
	must(t, err)
	must(t, verifyV2(b))
	info, err := apk.Inspect(out)
	must(t, err)
	if info.Label != "CLI App" {
		t.Errorf("label = %q", info.Label)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ---- Independent APK Signature Scheme v2 verifier (test only) ----

func verifyV2(b []byte) error {
	// Locate EOCD.
	eocd := -1
	for i := len(b) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(b[i:]) == 0x06054b50 {
			eocd = i
			break
		}
	}
	if eocd < 0 {
		return errors.New("no EOCD")
	}
	cdOff := int(binary.LittleEndian.Uint32(b[eocd+16:]))
	if cdOff < 32 || cdOff > eocd || string(b[cdOff-16:cdOff]) != "APK Sig Block 42" {
		return errors.New("no APK signing block")
	}
	size := int(binary.LittleEndian.Uint64(b[cdOff-24:]))
	start := cdOff - size - 8
	if start < 0 || int(binary.LittleEndian.Uint64(b[start:])) != size {
		return errors.New("bad signing block size")
	}
	pairs := b[start+8 : cdOff-24]
	var v2 []byte
	for len(pairs) > 0 {
		n := int(binary.LittleEndian.Uint64(pairs))
		id := binary.LittleEndian.Uint32(pairs[8:])
		if id == 0x7109871a {
			v2 = pairs[12 : 8+n]
		}
		pairs = pairs[8+n:]
	}
	if v2 == nil {
		return errors.New("no v2 block")
	}
	signers, err := lenPrefixed(&v2)
	if err != nil {
		return err
	}
	signer, err := lenPrefixed(&signers)
	if err != nil {
		return err
	}
	signedData, err := lenPrefixed(&signer)
	if err != nil {
		return err
	}
	sigs, err := lenPrefixed(&signer)
	if err != nil {
		return err
	}
	pubDER, err := lenPrefixed(&signer)
	if err != nil {
		return err
	}
	pub, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		return err
	}
	rpub := pub.(*rsa.PublicKey)
	sig, err := lenPrefixed(&sigs)
	if err != nil {
		return err
	}
	if alg := binary.LittleEndian.Uint32(sig); alg != 0x0103 {
		return fmt.Errorf("unexpected algorithm %#x", alg)
	}
	sig = sig[4:]
	sigBytes, err := lenPrefixed(&sig)
	if err != nil {
		return err
	}
	h := sha256.Sum256(signedData)
	if err := rsa.VerifyPKCS1v15(rpub, crypto.SHA256, h[:], sigBytes); err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	sd := signedData
	digests, err := lenPrefixed(&sd)
	if err != nil {
		return err
	}
	certs, err := lenPrefixed(&sd)
	if err != nil {
		return err
	}
	certDER, err := lenPrefixed(&certs)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return err
	}
	if !cert.PublicKey.(*rsa.PublicKey).Equal(rpub) {
		return errors.New("certificate key differs from signer public key")
	}
	d, err := lenPrefixed(&digests)
	if err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(d) != 0x0103 {
		return errors.New("digest algorithm mismatch")
	}
	d = d[4:]
	want, err := lenPrefixed(&d)
	if err != nil {
		return err
	}
	// Recompute: EOCD's CD offset must point at the signing block start.
	eocdCopy := append([]byte(nil), b[eocd:]...)
	binary.LittleEndian.PutUint32(eocdCopy[16:], uint32(start))
	var chunkDigests []byte
	n := 0
	for _, sec := range [][]byte{b[:start], b[cdOff:eocd], eocdCopy} {
		for len(sec) > 0 {
			c := sec[:min(len(sec), 1<<20)]
			sec = sec[len(c):]
			hh := sha256.New()
			hh.Write([]byte{0xa5})
			binary.Write(hh, binary.LittleEndian, uint32(len(c)))
			hh.Write(c)
			chunkDigests = hh.Sum(chunkDigests)
			n++
		}
	}
	top := sha256.New()
	top.Write([]byte{0x5a})
	binary.Write(top, binary.LittleEndian, uint32(n))
	top.Write(chunkDigests)
	if got := top.Sum(nil); !bytes.Equal(got, want) {
		return fmt.Errorf("content digest mismatch")
	}
	return nil
}

func lenPrefixed(b *[]byte) ([]byte, error) {
	if len(*b) < 4 {
		return nil, io.ErrUnexpectedEOF
	}
	n := int(binary.LittleEndian.Uint32(*b))
	if n > len(*b)-4 {
		return nil, io.ErrUnexpectedEOF
	}
	v := (*b)[4 : 4+n]
	*b = (*b)[4+n:]
	return v, nil
}
