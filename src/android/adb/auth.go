package adb

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	// KeyBits is the RSA key size adbd accepts.
	KeyBits = 2048
	// tokenSize is the size of the AUTH token adbd sends.
	tokenSize = 20
	// androidPubKeyWords is the modulus size in 32-bit words.
	androidPubKeyWords = KeyBits / 32
	androidPubKeySize  = 4 + 4 + KeyBits/8 + KeyBits/8 + 4

	// DefaultKeyComment is appended to the public key sent to the device; it
	// appears in the "Allow USB debugging?" dialog.
	DefaultKeyComment = "apkinspector@host"
)

// GenerateKey creates a new RSA-2048 key suitable for ADB authentication.
// A nil random source means crypto/rand.
func GenerateKey(random io.Reader) (*rsa.PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}
	k, err := rsa.GenerateKey(random, KeyBits)
	if err != nil {
		return nil, fmt.Errorf("adb: generating RSA key: %w", err)
	}
	return k, nil
}

// EncodePrivateKeyPEM encodes key as a PKCS#8 PEM block (the format of
// ~/.android/adbkey).
func EncodePrivateKeyPEM(key *rsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("adb: encoding private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// DecodePrivateKeyPEM parses a PKCS#8 or PKCS#1 PEM-encoded RSA-2048 key.
func DecodePrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, errors.New("adb: no PEM block found in key data")
	}
	var key *rsa.PrivateKey
	switch blk.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("adb: parsing PKCS#8 key: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("adb: key is %T, want RSA", k)
		}
		key = rk
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("adb: parsing PKCS#1 key: %w", err)
		}
		key = k
	default:
		return nil, fmt.Errorf("adb: unsupported PEM block type %q", blk.Type)
	}
	if key.N.BitLen() != KeyBits {
		return nil, fmt.Errorf("adb: RSA key has %d bits, adbd requires %d", key.N.BitLen(), KeyBits)
	}
	return key, nil
}

// SignToken signs an AUTH token like adb does: RSA PKCS#1 v1.5 with the
// token used directly as a SHA-1 digest.
func SignToken(key *rsa.PrivateKey, token []byte) ([]byte, error) {
	if len(token) != tokenSize {
		return nil, fmt.Errorf("adb: AUTH token has %d bytes, want %d", len(token), tokenSize)
	}
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA1, token)
	if err != nil {
		return nil, fmt.Errorf("adb: signing AUTH token: %w", err)
	}
	return sig, nil
}

// EncodeAndroidPublicKey encodes pub in Android's mincrypt RSAPublicKey
// format, base64-encoded and followed by " comment" (adb_auth_keygen format,
// without the trailing NUL).
func EncodeAndroidPublicKey(pub *rsa.PublicKey, comment string) (string, error) {
	if pub.N.BitLen() != KeyBits {
		return "", fmt.Errorf("adb: RSA key has %d bits, adbd requires %d", pub.N.BitLen(), KeyBits)
	}
	b := make([]byte, androidPubKeySize)
	binary.LittleEndian.PutUint32(b[0:], androidPubKeyWords)
	// n0inv = -1 / n[0] mod 2^32
	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0 := new(big.Int).Mod(pub.N, r32)
	inv := new(big.Int).ModInverse(n0, r32)
	if inv == nil {
		return "", errors.New("adb: RSA modulus is even")
	}
	n0inv := new(big.Int).Sub(r32, inv)
	binary.LittleEndian.PutUint32(b[4:], uint32(n0inv.Uint64()))
	putLE(b[8:8+KeyBits/8], pub.N)
	// rr = (2^(KeyBits))^2 mod n
	rr := new(big.Int).Lsh(big.NewInt(1), 2*KeyBits)
	rr.Mod(rr, pub.N)
	putLE(b[8+KeyBits/8:8+2*KeyBits/8], rr)
	binary.LittleEndian.PutUint32(b[8+2*KeyBits/8:], uint32(pub.E))
	s := base64.StdEncoding.EncodeToString(b)
	if comment != "" {
		s += " " + comment
	}
	return s, nil
}

// DecodeAndroidPublicKey parses the output of EncodeAndroidPublicKey (an
// adbkey.pub line or an AUTH RSAPUBLICKEY payload).
func DecodeAndroidPublicKey(s string) (*rsa.PublicKey, error) {
	s = strings.TrimRight(s, "\x00\r\n")
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("adb: public key is not base64: %w", err)
	}
	if len(b) != androidPubKeySize || binary.LittleEndian.Uint32(b) != androidPubKeyWords {
		return nil, errors.New("adb: public key has an unexpected size")
	}
	n := getLE(b[8 : 8+KeyBits/8])
	e := binary.LittleEndian.Uint32(b[8+2*KeyBits/8:])
	if e < 3 || e&1 == 0 || n.Sign() == 0 {
		return nil, errors.New("adb: public key has an invalid exponent or modulus")
	}
	return &rsa.PublicKey{N: n, E: int(e)}, nil
}

func putLE(dst []byte, v *big.Int) {
	be := v.FillBytes(make([]byte, len(dst)))
	for i := range dst {
		dst[i] = be[len(be)-1-i]
	}
}

func getLE(src []byte) *big.Int {
	be := make([]byte, len(src))
	for i := range src {
		be[len(src)-1-i] = src[i]
	}
	return new(big.Int).SetBytes(be)
}
