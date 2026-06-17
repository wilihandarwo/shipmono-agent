package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestGenerateKeyAndCSR(t *testing.T) {
	keyPEM, csrPEM, err := GenerateKeyAndCSR("test-cn")
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("csr PEM = %q", csrPEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != "test-cn" {
		t.Errorf("cn = %q", csr.Subject.CommonName)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Errorf("csr signature: %v", err)
	}
	if b, _ := pem.Decode(keyPEM); b == nil || b.Type != "PRIVATE KEY" {
		t.Errorf("key PEM = %q", keyPEM)
	}
}

func TestHolderSwapAndNotAfter(t *testing.T) {
	certPEM1, keyPEM1, na1 := selfSigned(t, time.Now().Add(24*time.Hour))
	h, err := NewHolder(certPEM1, keyPEM1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.NotAfter()
	if err != nil {
		t.Fatal(err)
	}
	if got.Unix() != na1.Unix() {
		t.Errorf("notAfter = %v, want %v", got, na1)
	}

	// Swap in a cert with a later expiry; the holder must reflect it.
	certPEM2, keyPEM2, na2 := selfSigned(t, time.Now().Add(240*time.Hour))
	if err := h.Set(certPEM2, keyPEM2); err != nil {
		t.Fatal(err)
	}
	got2, _ := h.NotAfter()
	if got2.Unix() != na2.Unix() {
		t.Errorf("after swap notAfter = %v, want %v", got2, na2)
	}
}

func TestTLSConfigPinsCA(t *testing.T) {
	certPEM, keyPEM, _ := selfSigned(t, time.Now().Add(time.Hour))
	h, err := NewHolder(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := TLSConfig(h, certPEM) // reuse the leaf as a stand-in CA bundle
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs not set")
	}
	chosen, err := cfg.GetClientCertificate(nil)
	if err != nil || len(chosen.Certificate) == 0 {
		t.Fatalf("GetClientCertificate returned %v, %v", chosen, err)
	}

	if _, err := TLSConfig(h, []byte("not a pem")); err == nil {
		t.Error("expected error for an empty CA bundle")
	}
}

// selfSigned makes a throwaway leaf+key for the holder tests.
func selfSigned(t *testing.T, notAfter time.Time) (certPEM, keyPEM []byte, na time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	leaf, _ := x509.ParseCertificate(der)
	return certPEM, keyPEM, leaf.NotAfter
}
