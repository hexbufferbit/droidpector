package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableDataDir(t *testing.T) {
	dir := t.TempDir()
	if got := portableDataDir(dir); got != "" {
		t.Fatalf("no runtime folder: must not be portable, got %q", got)
	}
	os.Mkdir(filepath.Join(dir, "runtime"), 0o755)
	if got := portableDataDir(dir); got != filepath.Join(dir, "data") {
		t.Fatalf("portable layout: got %q", got)
	}
	p := NewPaths(dir, filepath.Join(dir, "runtime"), filepath.Join(dir, "data"))
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.LogDir, p.SandboxDir, p.SessionDir, p.KeysDir, p.TempDir} {
		if rel, err := filepath.Rel(dir, d); err != nil || rel[:4] != "data" {
			t.Fatalf("%s is outside the portable folder", d)
		}
	}
}
