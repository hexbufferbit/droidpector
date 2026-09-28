// Command apkbuild builds a signed, installable APK without the Android SDK.
//
//	go run ./tools/apkbuild -manifest AndroidManifest.xml -dex classes.dex \
//	    -string app_name="Test App" -out app.apk [-key key.pem] [-assets dir]
//
// Without -key, a fresh RSA-2048 key and self-signed certificate are generated
// (use -save-key to keep it for reproducible re-signing).
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/droidpector/apkinspector/tools/apkbuild/apkbuild"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "apkbuild:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fset := flag.NewFlagSet("apkbuild", flag.ContinueOnError)
	var (
		manifest = fset.String("manifest", "", "textual AndroidManifest.xml (required)")
		out      = fset.String("out", "", "output APK path (required)")
		keyPath  = fset.String("key", "", "PEM RSA private key (PKCS#1/PKCS#8), optionally followed by its certificate")
		saveKey  = fset.String("save-key", "", "write the generated key to this PEM file (when -key is not given)")
		assets   = fset.String("assets", "", "directory whose files are added under assets/")
		unsigned = fset.Bool("unsigned", false, "do not sign the APK")
		utf8     = fset.Bool("utf8", true, "encode string pools as UTF-8")
		dex      multiFlag
		strs     multiFlag
		extra    multiFlag
	)
	fset.Var(&dex, "dex", "classes.dex file (repeatable: classes.dex, classes2.dex, …)")
	fset.Var(&strs, "string", "string resource name=value (repeatable), referenced as @string/name")
	fset.Var(&extra, "file", "extra entry zipPath=localPath (repeatable), e.g. lib/x86_64/libfoo.so=out/libfoo.so")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *manifest == "" || *out == "" {
		fset.Usage()
		return errors.New("-manifest and -out are required")
	}
	mx, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	spec := apkbuild.Spec{ManifestXML: mx, AXML: apkbuild.AXMLOptions{UTF8: *utf8}}
	for _, s := range strs {
		name, val, ok := strings.Cut(s, "=")
		if !ok || name == "" {
			return fmt.Errorf("-string %q: expected name=value", s)
		}
		spec.Strings = append(spec.Strings, apkbuild.StringResource{Name: name, Values: []apkbuild.LocalizedValue{{Value: val}}})
	}
	for i, d := range dex {
		b, err := os.ReadFile(d)
		if err != nil {
			return err
		}
		name := "classes.dex"
		if i > 0 {
			name = fmt.Sprintf("classes%d.dex", i+1)
		}
		spec.Files = append(spec.Files, apkbuild.File{Name: name, Data: b})
	}
	for _, e := range extra {
		zp, lp, ok := strings.Cut(e, "=")
		if !ok || zp == "" {
			return fmt.Errorf("-file %q: expected zipPath=localPath", e)
		}
		b, err := os.ReadFile(lp)
		if err != nil {
			return err
		}
		spec.Files = append(spec.Files, apkbuild.File{Name: zp, Data: b, Store: strings.HasSuffix(zp, ".so")})
	}
	if *assets != "" {
		err := filepath.WalkDir(*assets, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(*assets, p)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			spec.Files = append(spec.Files, apkbuild.File{Name: "assets/" + filepath.ToSlash(rel), Data: b})
			return nil
		})
		if err != nil {
			return fmt.Errorf("reading assets: %w", err)
		}
	}
	if !*unsigned {
		signer, err := loadSigner(*keyPath, *saveKey)
		if err != nil {
			return err
		}
		spec.Signer = signer
	}
	apk, err := apkbuild.Build(spec)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, apk, 0o644)
}

func loadSigner(keyPath, saveKey string) (*apkbuild.Signer, error) {
	if keyPath != "" {
		b, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		key, cert, err := apkbuild.ParseKeyPEM(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", keyPath, err)
		}
		if cert != nil {
			return &apkbuild.Signer{Key: key, Cert: cert}, nil
		}
		return apkbuild.NewSelfSigned(key)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	if saveKey != "" {
		pemKey, err := apkbuild.EncodeKeyPEM(key)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(saveKey, pemKey, 0o600); err != nil {
			return nil, err
		}
	}
	return apkbuild.NewSelfSigned(key)
}
