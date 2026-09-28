//go:build !windows

package platform

// ProtectSecret is a no-op outside Windows; secrets rely on 0600 permissions
// in the per-user data directory (development platforms only).
func ProtectSecret(data []byte) ([]byte, error) { return append([]byte(nil), data...), nil }

// UnprotectSecret reverses ProtectSecret.
func UnprotectSecret(data []byte) ([]byte, error) { return append([]byte(nil), data...), nil }
