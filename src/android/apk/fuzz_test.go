package apk_test

import (
	"bytes"
	"testing"

	"github.com/droidpector/apkinspector/src/android/apk"
	b "github.com/droidpector/apkinspector/tools/apkbuild/apkbuild"
)

func FuzzParseAXML(f *testing.F) {
	m := manifest("com.fuzz", []b.Attr{b.A("versionCode", b.Int(1))}, []b.Attr{b.A("label", b.Str("L"))}, launcherActivity(".M"))
	for _, opts := range []b.AXMLOptions{{UTF8: true}, {UTF8: false}, {StripAttrNames: true}} {
		enc, err := b.EncodeAXML(m, opts)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(enc)
	}
	f.Add([]byte{3, 0, 8, 0, 8, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		root, err := apk.ParseAXML(data)
		if err == nil && root == nil {
			t.Fatal("nil root without error")
		}
	})
}

func FuzzInspect(f *testing.F) {
	strs := []b.StringResource{{Name: "app_name", Values: []b.LocalizedValue{{Value: "Fuzz"}, {Locale: "de", Value: "Fuzz DE"}}}}
	ids := labelTable(f, strs)
	m := manifest("com.fuzz", nil, []b.Attr{b.A("label", b.Ref(ids["string/app_name"]))}, launcherActivity(".M"))
	f.Add(build(f, m, buildOpts{strings: strs, files: []b.File{{Name: "lib/arm64-v8a/liba.so", Data: []byte("x"), Store: true}}}))
	f.Add(build(f, m, buildOpts{unsigned: true, strings: strs, utf8: true}))
	f.Add([]byte("PK\x05\x06" + string(make([]byte, 18))))
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := apk.InspectReader(bytes.NewReader(data), int64(len(data)))
		if err == nil {
			_ = info.RequiredRuntime(false)
			_ = info.Err()
		}
	})
}

// FuzzResourceTable exercises resources.arsc parsing through a fixed manifest
// that references a string resource.
func FuzzResourceTable(f *testing.F) {
	strs := []b.StringResource{{Name: "app_name", Values: []b.LocalizedValue{{Value: "Fuzz"}}}}
	rt, err := b.EncodeStringTable("com.fuzz", strs, false)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(rt.ARSC)
	m := manifest("com.fuzz", nil, []b.Attr{b.A("label", b.Ref(rt.IDs["string/app_name"]))}, launcherActivity(".M"))
	axml, err := b.EncodeAXML(m, b.AXMLOptions{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, arsc []byte) {
		z, err := b.BuildZip([]b.File{{Name: "AndroidManifest.xml", Data: axml}, {Name: "resources.arsc", Data: arsc, Store: true}})
		if err != nil {
			t.Skip()
		}
		if _, err := apk.InspectReader(bytes.NewReader(z), int64(len(z))); err != nil {
			t.Fatalf("valid manifest rejected because of resources.arsc: %v", err)
		}
	})
}
