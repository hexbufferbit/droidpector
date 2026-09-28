//go:build windows

package platform

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProtectSecret encrypts data for the current Windows user (DPAPI), so keys
// at rest are unreadable to other accounts and off-machine copies.
func ProtectSecret(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: ptr(data)}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// UnprotectSecret reverses ProtectSecret.
func UnprotectSecret(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data)), Data: ptr(data)}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func ptr(b []byte) *byte {
	if len(b) == 0 {
		return nil
	}
	return &b[0]
}
