package vm

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// DataDiskPath returns the writable disk of a profile's sandbox.
func DataDiskPath(sandboxDir string, p Profile) string {
	return filepath.Join(sandboxDir, p.Name, "data.qcow2")
}

// PrepareDataDisk makes sure the sandbox's writable disk exists, creating it
// from the profile's pre-formatted template (a small sparse qcow2) on first
// use. The template is copied rather than used as a backing file so the
// sandbox survives application upgrades and moves.
func PrepareDataDisk(sandboxDir string, p Profile) (string, error) {
	dst := DataDiskPath(sandboxDir, p)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("checking the sandbox disk: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", fmt.Errorf("creating the sandbox folder: %w", err)
	}
	if err := copyFile(p.Path(p.DataTemplate), dst); err != nil {
		return "", fmt.Errorf("creating the sandbox disk from the runtime template: %w", err)
	}
	return dst, nil
}

// ResetDataDisk deletes the sandbox disk (apps, data and snapshots) and
// recreates it from the template. The VM must be stopped.
func ResetDataDisk(sandboxDir string, p Profile) (string, error) {
	if err := os.Remove(DataDiskPath(sandboxDir, p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("deleting the sandbox disk (is the sandbox still running?): %w", err)
	}
	return PrepareDataDisk(sandboxDir, p)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
