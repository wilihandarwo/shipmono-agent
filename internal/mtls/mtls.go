// Package mtls handles the agent's client-certificate identity for the mutual-TLS
// channel (control-plane security architecture §4). The agent generates its own EC keypair
// and a CSR; the control plane signs it via the private CA and returns the leaf. The private
// key never leaves the box. The cert's identity to the control plane is its fingerprint, not
// its subject, and the proxy (Caddy) verifies only that it chains to the CA — so the CSR
// common name is cosmetic.
package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"sync"
	"time"
)

// DefaultCN is the CSR common name (cosmetic — see package doc).
const DefaultCN = "shipmono-agent"

// GenerateKeyAndCSR makes a fresh EC P-256 key and a PEM CSR for it.
func GenerateKeyAndCSR(cn string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: marshal key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	tmpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: create csr: %w", err)
	}
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	return keyPEM, csrPEM, nil
}

// Holder keeps the current client cert behind a lock so renewal can swap it without
// disrupting the poll loop; new TLS handshakes pick up the new cert via GetClientCertificate
// (existing keep-alive connections age out naturally).
type Holder struct {
	mu   sync.RWMutex
	cert tls.Certificate
}

// NewHolder loads a leaf(+chain) PEM and its key into a swappable holder.
func NewHolder(certPEM, keyPEM []byte) (*Holder, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("mtls: load keypair: %w", err)
	}
	return &Holder{cert: c}, nil
}

// Set atomically swaps in a renewed cert/key.
func (h *Holder) Set(certPEM, keyPEM []byte) error {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("mtls: swap keypair: %w", err)
	}
	h.mu.Lock()
	h.cert = c
	h.mu.Unlock()
	return nil
}

func (h *Holder) current() *tls.Certificate {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c := h.cert
	return &c
}

// NotAfter returns the leaf's expiry, for renewal scheduling.
func (h *Holder) NotAfter() (time.Time, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.cert.Certificate) == 0 {
		return time.Time{}, fmt.Errorf("mtls: holder has no certificate")
	}
	leaf, err := x509.ParseCertificate(h.cert.Certificate[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("mtls: parse leaf: %w", err)
	}
	return leaf.NotAfter, nil
}

// TLSConfig builds a client TLS config that presents the held cert and pins the given CA
// bundle (so a mis-issued public cert for the agent endpoint is rejected).
func TLSConfig(h *Holder, caPEM []byte) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("mtls: no certificates in CA bundle")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return h.current(), nil
		},
	}, nil
}
