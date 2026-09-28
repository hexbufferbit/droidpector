package network

import (
	"container/list"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// CA is the per-boot interception certificate authority. Its private key
// lives only in memory (see ADR-006): it is never written to disk and a new
// one is generated whenever the sandbox is (re)started.
type CA struct {
	cert    *x509.Certificate
	certDER []byte
	key     crypto.Signer
	leafKey crypto.Signer // shared leaf key: generating one per host is costly and adds no security here

	mu    sync.Mutex
	cache map[string]*list.Element
	lru   *list.List
	max   int
}

type leafEntry struct {
	host string
	cert *tls.Certificate
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// NewCA generates a fresh CA valid from now-1d for one year.
func NewCA(now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         "droidpector Sandbox CA " + now.UTC().Format("2006-01-02 15:04:05"),
			Organization:       []string{"droidpector"},
			OrganizationalUnit: []string{"Sandbox interception CA — valid only inside the Android sandbox"},
		},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, certDER: der, key: key, leafKey: leafKey, cache: map[string]*list.Element{}, lru: list.New(), max: 2048}, nil
}

// Certificate returns the CA certificate.
func (ca *CA) Certificate() *x509.Certificate { return ca.cert }

// CertPEM returns the CA certificate in PEM form (for installation in the guest).
func (ca *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certDER})
}

// Fingerprint returns the SHA-256 fingerprint of the CA certificate.
func (ca *CA) Fingerprint() string {
	sum := sha256.Sum256(ca.certDER)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Leaf returns (and caches) a certificate for host (DNS name or IP literal).
func (ca *CA) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, fmt.Errorf("cannot issue a certificate without a host name")
	}
	ca.mu.Lock()
	if el, ok := ca.cache[host]; ok {
		ca.lru.MoveToFront(el)
		c := el.Value.(*leafEntry).cert
		ca.mu.Unlock()
		return c, nil
	}
	ca.mu.Unlock()

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"droidpector sandbox interception"}},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              minTime(now.Add(90*24*time.Hour), ca.cert.NotAfter),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, ca.leafKey.Public(), ca.key)
	if err != nil {
		return nil, fmt.Errorf("issuing certificate for %s: %w", host, err)
	}
	leaf, _ := x509.ParseCertificate(der)
	c := &tls.Certificate{Certificate: [][]byte{der, ca.certDER}, PrivateKey: ca.leafKey, Leaf: leaf}

	ca.mu.Lock()
	defer ca.mu.Unlock()
	if el, ok := ca.cache[host]; ok { // raced with another issuer
		return el.Value.(*leafEntry).cert, nil
	}
	ca.cache[host] = ca.lru.PushFront(&leafEntry{host: host, cert: c})
	for ca.lru.Len() > ca.max {
		old := ca.lru.Back()
		ca.lru.Remove(old)
		delete(ca.cache, old.Value.(*leafEntry).host)
	}
	return c, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
