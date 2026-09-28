package device

import (
	"context"
	"crypto/md5"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

// SubjectHashOld computes OpenSSL's X509_subject_name_hash_old for cert: the
// first four bytes of MD5(DER(subject)) read little-endian. Android names
// system CA files "<hash>.0" with this value.
func SubjectHashOld(cert *x509.Certificate) string {
	sum := md5.Sum(cert.RawSubject)
	v := uint32(sum[0]) | uint32(sum[1])<<8 | uint32(sum[2])<<16 | uint32(sum[3])<<24
	return fmt.Sprintf("%08x", v)
}

func parsePEMCert(pemCert []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemCert)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("the CA certificate is not PEM encoded")
	}
	return x509.ParseCertificate(block.Bytes)
}

const (
	systemCACerts = "/system/etc/security/cacerts"
	apexCACerts   = "/apex/com.android.conscrypt/cacerts"
	caStaging     = "/data/local/tmp/apkinspector-cacerts"
)

// InstallCA adds pemCert to the guest's system trust store without touching
// any disk image: the store is copied to a tmpfs mounted over the system
// directory (gone on reboot). On Android 14+ the Conscrypt APEX store is
// overlaid as well and propagated into zygote mount namespaces (best effort).
// It is idempotent.
func (d *Device) InstallCA(ctx context.Context, pemCert []byte) error {
	cert, err := parsePEMCert(pemCert)
	if err != nil {
		return err
	}
	name := SubjectHashOld(cert) + ".0"
	tmp := caStaging + "/" + name
	remote, cleanup, err := d.uploadBytes(ctx, pemCert)
	if err != nil {
		return fmt.Errorf("uploading the sandbox CA: %w", err)
	}
	defer cleanup()
	script := strings.Join([]string{
		"set -e",
		"rm -rf " + caStaging + " && mkdir -p " + caStaging,
		"cp " + systemCACerts + "/* " + caStaging + "/",
		"cp " + remote + " " + tmp,
		// Mount a tmpfs over the store only once; later calls just refresh it.
		"if ! grep -q ' " + systemCACerts + " tmpfs' /proc/mounts; then mount -t tmpfs tmpfs " + systemCACerts + "; fi",
		"rm -f " + systemCACerts + "/*",
		"cp " + caStaging + "/* " + systemCACerts + "/",
		"chown root:root " + systemCACerts + "/*",
		"chmod 644 " + systemCACerts + "/*",
		"chcon u:object_r:system_file:s0 " + systemCACerts + " " + systemCACerts + "/* 2>/dev/null || true",
		// Android 14+: Conscrypt reads CAs from its APEX; bind our store over it
		// in init's and every zygote's mount namespace.
		"if [ -d " + apexCACerts + " ]; then " +
			"mount --bind " + systemCACerts + " " + apexCACerts + " 2>/dev/null || true; " +
			"for z in $(pidof zygote zygote64); do nsenter --mount=/proc/$z/ns/mnt -- mount --bind " + systemCACerts + " " + apexCACerts + " 2>/dev/null || true; done; " +
			"fi",
		"rm -rf " + caStaging,
	}, "\n")
	if _, err := d.runRootScript(ctx, script); err != nil {
		return fmt.Errorf("installing the sandbox CA into the system trust store: %w", err)
	}
	ok, err := d.VerifyCA(ctx, pemCert)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("the sandbox CA was installed but is not visible in %s", systemCACerts)
	}
	return nil
}

// VerifyCA reports whether exactly this certificate is in the system store.
func (d *Device) VerifyCA(ctx context.Context, pemCert []byte) (bool, error) {
	cert, err := parsePEMCert(pemCert)
	if err != nil {
		return false, err
	}
	r, err := d.run(ctx, "cat "+systemCACerts+"/"+SubjectHashOld(cert)+".0")
	if err != nil {
		return false, err
	}
	if r.code != 0 {
		return false, nil
	}
	got, err := parsePEMCert([]byte(r.stdout))
	if err != nil {
		return false, nil
	}
	return got.Equal(cert), nil
}

// RemoveCA restores the original system trust store by unmounting the tmpfs
// overlays. It is a no-op when no overlay is active.
func (d *Device) RemoveCA(ctx context.Context) error {
	script := strings.Join([]string{
		"if [ -d " + apexCACerts + " ]; then " +
			"for z in $(pidof zygote zygote64); do nsenter --mount=/proc/$z/ns/mnt -- umount " + apexCACerts + " 2>/dev/null || true; done; " +
			"umount " + apexCACerts + " 2>/dev/null || true; fi",
		"while grep -q ' " + systemCACerts + " tmpfs' /proc/mounts; do umount " + systemCACerts + " || break; done",
		"true",
	}, "\n")
	if _, err := d.runRootScript(ctx, script); err != nil {
		return fmt.Errorf("removing the sandbox CA: %w", err)
	}
	return nil
}
