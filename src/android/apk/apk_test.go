package apk_test

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/droidpector/apkinspector/src/android/apk"
	b "github.com/droidpector/apkinspector/tools/apkbuild/apkbuild"
)

var (
	signerOnce sync.Once
	signerVal  *b.Signer
)

func testSigner(t testing.TB) *b.Signer {
	t.Helper()
	signerOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		if signerVal, err = b.NewSelfSigned(k); err != nil {
			panic(err)
		}
	})
	return signerVal
}

func launcherActivity(name string) *b.Element {
	return b.El("activity", []b.Attr{b.A("name", b.Str(name))},
		b.El("intent-filter", nil,
			b.El("action", []b.Attr{b.A("name", b.Str("android.intent.action.MAIN"))}),
			b.El("category", []b.Attr{b.A("name", b.Str("android.intent.category.LAUNCHER"))}),
		))
}

func manifest(pkg string, manifestAttrs []b.Attr, appAttrs []b.Attr, appChildren ...*b.Element) *b.Element {
	attrs := append([]b.Attr{b.Plain("package", b.Str(pkg))}, manifestAttrs...)
	return b.El("manifest", attrs,
		b.El("uses-sdk", []b.Attr{b.A("minSdkVersion", b.Int(26)), b.A("targetSdkVersion", b.Int(33))}),
		b.El("uses-permission", []b.Attr{b.A("name", b.Str("android.permission.INTERNET"))}),
		b.El("uses-permission", []b.Attr{b.A("name", b.Str("android.permission.CAMERA"))}),
		b.El("application", appAttrs, appChildren...),
	)
}

type buildOpts struct {
	utf8     bool
	strip    bool
	unsigned bool
	strings  []b.StringResource
	files    []b.File
	noDex    bool
}

func build(t testing.TB, m *b.Element, o buildOpts) []byte {
	t.Helper()
	spec := b.Spec{Manifest: m, Strings: o.strings, AXML: b.AXMLOptions{UTF8: o.utf8, StripAttrNames: o.strip}}
	if !o.noDex {
		spec.Files = append(spec.Files, b.File{Name: "classes.dex", Data: []byte("dex\n035\x00")})
	}
	spec.Files = append(spec.Files, o.files...)
	if !o.unsigned {
		spec.Signer = testSigner(t)
	}
	out, err := b.Build(spec)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func inspect(t testing.TB, data []byte) *apk.Info {
	t.Helper()
	info, err := apk.InspectReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("InspectReader: %v", err)
	}
	return info
}

func hasIssue(info *apk.Info, code apk.IssueCode) *apk.Issue {
	for i := range info.Issues {
		if info.Issues[i].Code == code {
			return &info.Issues[i]
		}
	}
	return nil
}

// labelIDs returns the resource ID assigned to string/app_name.
func labelTable(t testing.TB, strs []b.StringResource) map[string]uint32 {
	rt, err := b.EncodeStringTable("x", strs, true)
	if err != nil {
		t.Fatal(err)
	}
	return rt.IDs
}

func TestManifestFields(t *testing.T) {
	for _, utf8 := range []bool{false, true} {
		name := "utf16"
		if utf8 {
			name = "utf8"
		}
		t.Run(name, func(t *testing.T) {
			m := manifest("com.example.app",
				[]b.Attr{b.A("versionCode", b.Int(7)), b.A("versionCodeMajor", b.Int(2)), b.A("versionName", b.Str("2.0-β"))},
				[]b.Attr{b.A("label", b.Str("Ünïcødé App")), b.A("extractNativeLibs", b.Bool(true)), b.A("debuggable", b.Bool(true))},
				b.El("activity", []b.Attr{b.A("name", b.Str(".Settings"))}),
				launcherActivity("Main"),
			)
			info := inspect(t, build(t, m, buildOpts{utf8: utf8}))
			if info.Package != "com.example.app" {
				t.Errorf("package = %q", info.Package)
			}
			if want := int64(2)<<32 | 7; info.VersionCode != want {
				t.Errorf("versionCode = %d, want %d", info.VersionCode, want)
			}
			if info.VersionName != "2.0-β" || info.Label != "Ünïcødé App" {
				t.Errorf("versionName/label = %q/%q", info.VersionName, info.Label)
			}
			if info.MinSDK != 26 || info.TargetSDK != 33 {
				t.Errorf("sdk = %d/%d", info.MinSDK, info.TargetSDK)
			}
			if info.LaunchableActivity != "com.example.app.Main" || info.Component() != "com.example.app/com.example.app.Main" {
				t.Errorf("launcher = %q / %q", info.LaunchableActivity, info.Component())
			}
			if strings.Join(info.Permissions, ",") != "android.permission.INTERNET,android.permission.CAMERA" {
				t.Errorf("permissions = %v", info.Permissions)
			}
			if info.ExtractNativeLibs == nil || !*info.ExtractNativeLibs || !info.Debuggable {
				t.Errorf("flags: extractNativeLibs=%v debuggable=%v", info.ExtractNativeLibs, info.Debuggable)
			}
			if !info.Signature.V2 || !info.Installable() {
				t.Errorf("signature %+v issues %+v", info.Signature, info.Issues)
			}
			if info.IsSplit() {
				t.Error("base APK reported as split")
			}
		})
	}
}

func TestObfuscatedAttributeNames(t *testing.T) {
	m := manifest("com.obf", []b.Attr{b.A("versionCode", b.Int(3))}, []b.Attr{b.A("label", b.Str("Obf"))}, launcherActivity("com.obf.A"))
	info := inspect(t, build(t, m, buildOpts{strip: true}))
	if info.VersionCode != 3 || info.Label != "Obf" || info.LaunchableActivity != "com.obf.A" || info.TargetSDK != 33 {
		t.Errorf("got %+v", info)
	}
}

func TestLabelFromResources(t *testing.T) {
	cases := []struct {
		name string
		strs []b.StringResource
		want string
	}{
		{"default config", []b.StringResource{
			{Name: "other", Values: []b.LocalizedValue{{Value: "x"}}},
			{Name: "app_name", Values: []b.LocalizedValue{{Locale: "fr", Value: "Appli"}, {Value: "My App"}}},
		}, "My App"},
		{"fallback to first config", []b.StringResource{
			{Name: "app_name", Values: []b.LocalizedValue{{Locale: "de", Value: "Meine App"}}},
		}, "Meine App"},
	}
	for _, tc := range cases {
		for _, utf8 := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				ids := labelTable(t, tc.strs)
				m := manifest("com.res", []b.Attr{b.A("versionName", b.Ref(ids["string/app_name"]))},
					[]b.Attr{b.A("label", b.Ref(ids["string/app_name"]))}, launcherActivity(".M"))
				info := inspect(t, build(t, m, buildOpts{utf8: utf8, strings: tc.strs}))
				if info.Label != tc.want || info.VersionName != tc.want {
					t.Errorf("label=%q versionName=%q, want %q", info.Label, info.VersionName, tc.want)
				}
			})
		}
	}
}

func TestUnresolvableReference(t *testing.T) {
	m := manifest("com.res", nil, []b.Attr{b.A("label", b.Ref(0x7f0100ff))}, launcherActivity(".M"))
	info := inspect(t, build(t, m, buildOpts{}))
	if info.Label != "@0x7f0100ff" {
		t.Errorf("label = %q", info.Label)
	}
}

func TestRuntimeDecision(t *testing.T) {
	cases := []struct {
		abis       []string
		arm        bool
		runtime    string
		translated bool
		issue      apk.IssueCode
	}{
		{nil, false, apk.RuntimeX86_64, false, ""},
		{[]string{"x86_64", "arm64-v8a"}, false, apk.RuntimeX86_64, false, ""},
		{[]string{"x86"}, false, apk.RuntimeX86_64, false, ""},
		{[]string{"arm64-v8a"}, false, apk.RuntimeARM64, false, apk.IssueARMOnly},
		{[]string{"armeabi-v7a", "arm64-v8a"}, true, apk.RuntimeX86_64, true, apk.IssueARMOnly},
		{[]string{"armeabi"}, false, apk.RuntimeARM64, false, apk.IssueARMOnly},
		{[]string{"mips"}, false, apk.RuntimeX86_64, false, apk.IssueNoSupportedABI},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.abis, "+"), func(t *testing.T) {
			var files []b.File
			for _, a := range tc.abis {
				files = append(files, b.File{Name: "lib/" + a + "/libfoo.so", Data: []byte("\x7fELF"), Store: true})
			}
			files = append(files, b.File{Name: "lib/README", Data: []byte("not a lib")})
			info := inspect(t, build(t, manifest("com.abi", nil, nil, launcherActivity(".M")), buildOpts{files: files}))
			d := info.RequiredRuntime(tc.arm)
			if d.Runtime != tc.runtime || d.Translated != tc.translated || d.Reason == "" {
				t.Errorf("decision = %+v", d)
			}
			if tc.issue != "" {
				is := hasIssue(info, tc.issue)
				if is == nil {
					t.Fatalf("missing issue %s in %+v", tc.issue, info.Issues)
				}
				if tc.issue == apk.IssueARMOnly && !strings.Contains(is.Message, "only for ARM") {
					t.Errorf("message = %q", is.Message)
				}
			}
		})
	}
}

func TestUnsigned(t *testing.T) {
	info := inspect(t, build(t, manifest("com.u", nil, nil, launcherActivity(".M")), buildOpts{unsigned: true}))
	is := hasIssue(info, apk.IssueUnsigned)
	if is == nil || is.Severity != apk.SeverityError || info.Installable() {
		t.Fatalf("issues = %+v", info.Issues)
	}
	var ve *apk.ValidationError
	if !errors.As(info.Err(), &ve) || ve.Issues[0].Code != apk.IssueUnsigned {
		t.Errorf("Err() = %v", info.Err())
	}
}

func TestV1SignatureDetection(t *testing.T) {
	files := []b.File{{Name: "META-INF/MANIFEST.MF", Data: []byte("Manifest-Version: 1.0\n")}, {Name: "META-INF/CERT.RSA", Data: []byte{0x30}}}
	m := manifest("com.v1", nil, nil, launcherActivity(".M"))
	info := inspect(t, build(t, m, buildOpts{unsigned: true, files: files}))
	if !info.Signature.V1 || info.Signature.V2 {
		t.Errorf("signature = %+v", info.Signature)
	}
	// targetSdk 33 with v1 only must be flagged.
	if is := hasIssue(info, apk.IssueV1OnlyTargetR); is == nil || is.Severity != apk.SeverityError {
		t.Errorf("issues = %+v", info.Issues)
	}
}

func TestSplitAPK(t *testing.T) {
	m := b.El("manifest", []b.Attr{b.Plain("package", b.Str("com.split")), b.Plain("split", b.Str("config.arm64_v8a")), b.A("isFeatureSplit", b.Bool(false))},
		b.El("application", []b.Attr{b.A("hasCode", b.Bool(false))}))
	info := inspect(t, build(t, m, buildOpts{noDex: true, files: []b.File{{Name: "lib/arm64-v8a/libx.so", Data: []byte("x"), Store: true}}}))
	if !info.IsSplit() || info.Split != "config.arm64_v8a" {
		t.Errorf("split = %q", info.Split)
	}
	if hasIssue(info, apk.IssueSplitAPK) == nil || hasIssue(info, apk.IssueNoDex) == nil {
		t.Errorf("issues = %+v", info.Issues)
	}
	if hasIssue(info, apk.IssueNoLauncher) != nil {
		t.Error("split APK should not warn about a missing launcher")
	}
}

func TestFatalErrors(t *testing.T) {
	var noManifest bytes.Buffer
	zw := zip.NewWriter(&noManifest)
	w, _ := zw.Create("classes.dex")
	w.Write([]byte("dex"))
	zw.Close()

	var badManifest bytes.Buffer
	zw = zip.NewWriter(&badManifest)
	w, _ = zw.Create("AndroidManifest.xml")
	w.Write([]byte("<manifest package=\"plain.text\"/>"))
	zw.Close()

	cases := []struct {
		name string
		data []byte
		code apk.IssueCode
	}{
		{"not a zip", []byte("this is definitely not a zip archive, just text"), apk.IssueNotZip},
		{"empty", nil, apk.IssueNotZip},
		{"missing manifest", noManifest.Bytes(), apk.IssueMissingManifest},
		{"text manifest", badManifest.Bytes(), apk.IssueInvalidManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := apk.InspectReader(bytes.NewReader(tc.data), int64(len(tc.data)))
			var ve *apk.ValidationError
			if !errors.As(err, &ve) || ve.Issues[0].Code != tc.code || ve.Issues[0].Severity != apk.SeverityError {
				t.Fatalf("err = %v", err)
			}
			if !apk.IsValidationError(err) {
				t.Error("IsValidationError = false")
			}
		})
	}
}

func TestInspectFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.apk")
	if err := os.WriteFile(p, build(t, manifest("com.file", nil, nil, launcherActivity(".M")), buildOpts{}), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := apk.Inspect(p)
	if err != nil || info.Package != "com.file" {
		t.Fatalf("Inspect: %v %+v", err, info)
	}
	if _, err := apk.Inspect(filepath.Join(t.TempDir(), "missing.apk")); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := apk.Inspect(t.TempDir()); err == nil {
		t.Error("directory accepted")
	}
}

func TestDisabledAndAliasLauncher(t *testing.T) {
	disabled := launcherActivity(".Disabled")
	disabled.Attrs = append(disabled.Attrs, b.A("enabled", b.Bool(false)))
	alias := launcherActivity(".Alias")
	alias.Name = "activity-alias"
	m := manifest("com.alias", nil, nil, disabled, alias)
	info := inspect(t, build(t, m, buildOpts{}))
	if info.LaunchableActivity != "com.alias.Alias" {
		t.Errorf("launcher = %q", info.LaunchableActivity)
	}
}
