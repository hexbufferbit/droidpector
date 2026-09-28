package apkbuild

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// APK Signature Scheme v2 constants.
const (
	SigBlockMagic = "APK Sig Block 42"
	// V2BlockID is the ID of the v2 signature in the APK Signing Block.
	V2BlockID uint32 = 0x7109871a
	// AlgRSAPKCS1SHA256 is RSASSA-PKCS1-v1_5 with SHA2-256.
	AlgRSAPKCS1SHA256 uint32 = 0x0103

	chunkSize = 1 << 20
)

// Signer is the key material used to sign an APK.
type Signer struct {
	Key  *rsa.PrivateKey
	Cert *x509.Certificate
}

// NewSelfSigned returns a Signer with a deterministic self-signed certificate
// for key (fixed validity, serial derived from the public key), so repeated
// builds with the same key are byte-identical.
func NewSelfSigned(key *rsa.PrivateKey) (*Signer, error) {
	if key == nil {
		return nil, errors.New("self-signed certificate: nil key")
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate: %w", err)
	}
	sum := sha256.Sum256(spki)
	serial := new(big.Int).SetBytes(sum[:16])
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: "droidpector TestApp", Organization: []string{"apkinspector"}},
		NotBefore:          time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:           time.Date(2070, 1, 1, 0, 0, 0, 0, time.UTC),
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("self-signed certificate: %w", err)
	}
	return &Signer{Key: key, Cert: cert}, nil
}

// ParseKeyPEM parses an RSA private key (PKCS#1 or PKCS#8 PEM) and, if
// present in the same data, a certificate.
func ParseKeyPEM(data []byte) (*rsa.PrivateKey, *x509.Certificate, error) {
	var (
		key  *rsa.PrivateKey
		cert *x509.Certificate
	)
	for {
		var blk *pem.Block
		blk, data = pem.Decode(data)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("parse RSA private key: %w", err)
			}
			key = k
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, nil, fmt.Errorf("parse private key: only RSA keys are supported, got %T", k)
			}
			key = rk
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				return nil, nil, fmt.Errorf("parse certificate: %w", err)
			}
			cert = c
		}
	}
	if key == nil {
		return nil, nil, errors.New("no RSA private key found in PEM data")
	}
	return key, cert, nil
}

// EncodeKeyPEM encodes key as PKCS#8 PEM.
func EncodeKeyPEM(key *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ZipSections are the byte ranges APK signing covers.
type ZipSections struct {
	CDOffset   int64 // start of central directory == end of section 1
	EOCDOffset int64
}

// FindZipSections locates the central directory and EOCD of an unsigned zip.
func FindZipSections(apk []byte) (ZipSections, error) {
	for i := len(apk) - 22; i >= 0 && i >= len(apk)-22-0xFFFF; i-- {
		if binary.LittleEndian.Uint32(apk[i:]) != 0x06054b50 {
			continue
		}
		if i+22+int(binary.LittleEndian.Uint16(apk[i+20:])) != len(apk) {
			continue
		}
		cd := int64(binary.LittleEndian.Uint32(apk[i+16:]))
		if cd > int64(i) {
			return ZipSections{}, errors.New("central directory offset beyond EOCD")
		}
		return ZipSections{CDOffset: cd, EOCDOffset: int64(i)}, nil
	}
	return ZipSections{}, errors.New("ZIP End of Central Directory record not found")
}

// ContentDigestV2 computes the v2 chunked SHA-256 content digest over the
// three ZIP sections. eocd must already hold the CD offset that the signed
// APK will record (the signing block start).
func ContentDigestV2(entries, cd, eocd []byte) []byte {
	var chunks [][]byte
	for _, sec := range [][]byte{entries, cd, eocd} {
		for off := 0; off < len(sec); off += chunkSize {
			end := min(off+chunkSize, len(sec))
			h := sha256.New()
			var pre [5]byte
			pre[0] = 0xa5
			binary.LittleEndian.PutUint32(pre[1:], uint32(end-off))
			h.Write(pre[:])
			h.Write(sec[off:end])
			chunks = append(chunks, h.Sum(nil))
		}
	}
	h := sha256.New()
	var pre [5]byte
	pre[0] = 0x5a
	binary.LittleEndian.PutUint32(pre[1:], uint32(len(chunks)))
	h.Write(pre[:])
	for _, c := range chunks {
		h.Write(c)
	}
	return h.Sum(nil)
}

// SignV2 signs an unsigned (zipaligned) APK with APK Signature Scheme v2 and
// returns the signed APK.
func SignV2(apk []byte, s *Signer) ([]byte, error) {
	if s == nil || s.Key == nil || s.Cert == nil {
		return nil, errors.New("sign APK: signer key and certificate are required")
	}
	pub, ok := s.Cert.PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&s.Key.PublicKey) {
		return nil, errors.New("sign APK: certificate does not match the private key")
	}
	sec, err := FindZipSections(apk)
	if err != nil {
		return nil, fmt.Errorf("sign APK: %w", err)
	}
	if sec.CDOffset >= 24 && string(apk[sec.CDOffset-16:sec.CDOffset]) == SigBlockMagic {
		return nil, errors.New("sign APK: the APK already has a signing block")
	}
	entries := apk[:sec.CDOffset]
	cd := apk[sec.CDOffset:sec.EOCDOffset]
	eocd := apk[sec.EOCDOffset:]
	digest := ContentDigestV2(entries, cd, eocd)

	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("sign APK: %w", err)
	}
	signedData := concat(
		lp(lp(concat(u32(AlgRSAPKCS1SHA256), lp(digest)))), // digests
		lp(lp(s.Cert.Raw)), // certificates
		lp(nil),            // additional attributes
	)
	h := sha256.Sum256(signedData)
	sig, err := rsa.SignPKCS1v15(nil, s.Key, crypto.SHA256, h[:])
	if err != nil {
		return nil, fmt.Errorf("sign APK: %w", err)
	}
	signer := concat(
		lp(signedData),
		lp(lp(concat(u32(AlgRSAPKCS1SHA256), lp(sig)))),
		lp(spki),
	)
	v2 := lp(lp(signer))

	pair := concat(u64(uint64(len(v2)+4)), u32(V2BlockID), v2)
	blockSize := uint64(len(pair) + 8 + 16)
	block := concat(u64(blockSize), pair, u64(blockSize), []byte(SigBlockMagic))

	out := make([]byte, 0, len(apk)+len(block))
	out = append(out, entries...)
	out = append(out, block...)
	out = append(out, cd...)
	newEOCD := bytes.Clone(eocd)
	binary.LittleEndian.PutUint32(newEOCD[16:], uint32(sec.CDOffset+int64(len(block))))
	out = append(out, newEOCD...)
	return out, nil
}

func lp(b []byte) []byte { return concat(u32(uint32(len(b))), b) }

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func concat(parts ...[]byte) []byte {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
