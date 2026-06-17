// Package credstore persists the agent credential (the permanent agent token,
// the server id, and the control-plane host) under SHIPMONO_HOME. The file is
// written 0600 and owned by the agent user — the same guarantee the Ruby
// simulator makes and the security doc (§6) requires.
package credstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the credential filename inside SHIPMONO_HOME.
const FileName = "credential.json"

// mTLS material filenames inside SHIPMONO_HOME (control-plane security architecture §4).
// All are written 0600 like the credential. The key never leaves the box.
const (
	KeyFileName  = "client.key"    // the agent's private key
	CertFileName = "client.crt"    // its signed leaf (+ chain) presented over mTLS
	CAFileName   = "ca_bundle.pem" // the control-plane CA the agent pins
)

// Mode is the required permission bits: read/write for the owner only.
const Mode fs.FileMode = 0o600

// Credential is what the agent persists after a successful pairing.
type Credential struct {
	Host       string `json:"host"`
	AgentToken string `json:"agent_token"`
	ServerID   int    `json:"server_id"`
	// AgentEndpoint is the mTLS host for all calls after registration (e.g.
	// "agents.shipmono.dev:8443"). Empty when the control plane isn't running mTLS
	// (dev/test), in which case the agent stays bearer-only against Host.
	AgentEndpoint string `json:"agent_endpoint,omitempty"`
}

// ErrNotPaired is returned by Load when no credential exists.
var ErrNotPaired = errors.New("credstore: not paired (no credential found)")

// Path returns the credential path for a given home directory.
func Path(home string) string { return filepath.Join(home, FileName) }

// Load reads and validates the credential. It returns ErrNotPaired if the file
// does not exist, and re-asserts 0600 (best effort) on the way in.
func Load(home string) (Credential, error) {
	p := Path(home)
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Credential{}, ErrNotPaired
	}
	if err != nil {
		return Credential{}, fmt.Errorf("credstore: stat: %w", err)
	}
	// Defense in depth: if the file somehow became group/world readable,
	// tighten it before reading the secret out.
	if info.Mode().Perm() != Mode {
		_ = os.Chmod(p, Mode)
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return Credential{}, fmt.Errorf("credstore: read: %w", err)
	}
	var c Credential
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credential{}, fmt.Errorf("credstore: parse: %w", err)
	}
	if c.AgentToken == "" || c.Host == "" {
		return Credential{}, errors.New("credstore: credential is missing host or agent_token")
	}
	return c, nil
}

// Save writes the credential atomically with mode 0600. It writes to a temp
// file in the same directory, fsyncs, then renames over the target so a crash
// never leaves a half-written secret.
func Save(home string, c Credential) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("credstore: mkdir home: %w", err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("credstore: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(home, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("credstore: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(Mode); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: chmod temp: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credstore: close temp: %w", err)
	}
	if err := os.Rename(tmpName, Path(home)); err != nil {
		return fmt.Errorf("credstore: rename: %w", err)
	}
	return nil
}

// Delete removes the credential and any mTLS material. Called when the control
// plane reports the identity as revoked. Absence is not an error (idempotent).
func Delete(home string) error {
	for _, name := range []string{FileName, CertFileName, KeyFileName, CAFileName} {
		if err := os.Remove(filepath.Join(home, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// SaveCertMaterial persists the mTLS leaf(+chain), private key, and CA bundle, each 0600.
func SaveCertMaterial(home string, certPEM, keyPEM, caPEM []byte) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("credstore: mkdir home: %w", err)
	}
	files := map[string][]byte{CertFileName: certPEM, KeyFileName: keyPEM, CAFileName: caPEM}
	for name, data := range files {
		if err := writeSecret(filepath.Join(home, name), data); err != nil {
			return err
		}
	}
	return nil
}

// LoadCertMaterial reads the mTLS leaf(+chain), key, and CA bundle.
func LoadCertMaterial(home string) (certPEM, keyPEM, caPEM []byte, err error) {
	if certPEM, err = os.ReadFile(filepath.Join(home, CertFileName)); err != nil {
		return nil, nil, nil, fmt.Errorf("credstore: read cert: %w", err)
	}
	if keyPEM, err = os.ReadFile(filepath.Join(home, KeyFileName)); err != nil {
		return nil, nil, nil, fmt.Errorf("credstore: read key: %w", err)
	}
	if caPEM, err = os.ReadFile(filepath.Join(home, CAFileName)); err != nil {
		return nil, nil, nil, fmt.Errorf("credstore: read ca: %w", err)
	}
	return certPEM, keyPEM, caPEM, nil
}

// HasCertMaterial reports whether all three mTLS files are present.
func HasCertMaterial(home string) bool {
	for _, name := range []string{CertFileName, KeyFileName, CAFileName} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			return false
		}
	}
	return true
}

// writeSecret writes data to path atomically with mode 0600 (temp file, fsync, rename).
func writeSecret(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("credstore: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(Mode); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: chmod temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("credstore: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credstore: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("credstore: rename: %w", err)
	}
	return nil
}
