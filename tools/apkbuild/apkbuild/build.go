package apkbuild

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
)

// Spec describes an APK to build.
type Spec struct {
	// ManifestXML is a textual AndroidManifest.xml. Either it or Manifest must
	// be set. "@string/name" values refer to Strings.
	ManifestXML []byte
	// Manifest is a pre-built element tree (alternative to ManifestXML).
	Manifest *Element
	// Package names the resource package in resources.arsc; defaults to the
	// manifest's package attribute.
	Package string
	// Strings become resources.arsc (omitted when empty).
	Strings []StringResource
	// Files are added as-is (classes.dex, assets/…, lib/<abi>/….so).
	Files []File
	// Signer signs the APK with scheme v2; nil leaves it unsigned.
	Signer *Signer
	// AXML controls the binary XML encoding.
	AXML AXMLOptions
}

// Build encodes the manifest and resources, assembles the archive and signs it.
func Build(spec Spec) ([]byte, error) {
	if (spec.ManifestXML == nil) == (spec.Manifest == nil) {
		return nil, errors.New("build APK: exactly one of ManifestXML and Manifest must be set")
	}
	pkg := spec.Package
	if pkg == "" {
		if spec.Manifest != nil {
			for _, a := range spec.Manifest.Attrs {
				if a.Namespace == "" && a.Name == "package" {
					pkg = a.Value.String
				}
			}
		} else {
			pkg = manifestPackage(spec.ManifestXML)
		}
	}
	var files []File
	var ids map[string]uint32
	if len(spec.Strings) > 0 {
		rt, err := EncodeStringTable(pkg, spec.Strings, spec.AXML.UTF8)
		if err != nil {
			return nil, fmt.Errorf("build APK: %w", err)
		}
		ids = rt.IDs
		// Android 11+ requires resources.arsc stored uncompressed and 4-aligned.
		files = append(files, File{Name: "resources.arsc", Data: rt.ARSC, Store: true, Align: 4})
	}
	root := spec.Manifest
	if root == nil {
		var err error
		if root, err = ParseManifestXML(bytes.NewReader(spec.ManifestXML), ids); err != nil {
			return nil, fmt.Errorf("build APK: %w", err)
		}
	}
	axml, err := EncodeAXML(root, spec.AXML)
	if err != nil {
		return nil, fmt.Errorf("build APK: %w", err)
	}
	files = append([]File{{Name: "AndroidManifest.xml", Data: axml}}, files...)
	files = append(files, spec.Files...)
	zipped, err := BuildZip(files)
	if err != nil {
		return nil, fmt.Errorf("build APK: %w", err)
	}
	if spec.Signer == nil {
		return zipped, nil
	}
	signed, err := SignV2(zipped, spec.Signer)
	if err != nil {
		return nil, fmt.Errorf("build APK: %w", err)
	}
	return signed, nil
}

func manifestPackage(doc []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			for _, a := range se.Attr {
				if a.Name.Space == "" && a.Name.Local == "package" {
					return a.Value
				}
			}
			return ""
		}
	}
}
