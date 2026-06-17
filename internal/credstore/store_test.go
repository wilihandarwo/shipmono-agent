package credstore

import (
	"errors"
	"os"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	home := t.TempDir()
	want := Credential{Host: "https://app.example", AgentToken: "agt_secret", ServerID: 42}
	if err := Save(home, want); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != Mode {
		t.Errorf("credential mode = %o, want %o", perm, Mode)
	}

	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("loaded %+v, want %+v", got, want)
	}
}

func TestLoadNotPaired(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
}

func TestLoadTightensLoosePermissions(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Credential{Host: "h", AgentToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(Path(home))
	if perm := info.Mode().Perm(); perm != Mode {
		t.Errorf("Load should re-assert 0600, got %o", perm)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	home := t.TempDir()
	if err := Delete(home); err != nil {
		t.Errorf("deleting absent credential should be a no-op, got %v", err)
	}
	if err := Save(home, Credential{Host: "h", AgentToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(home); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); !errors.Is(err, ErrNotPaired) {
		t.Errorf("after delete, Load err = %v, want ErrNotPaired", err)
	}
}

func TestCertMaterialRoundtrip(t *testing.T) {
	home := t.TempDir()
	if HasCertMaterial(home) {
		t.Fatal("should have no cert material initially")
	}

	cert, key, ca := []byte("CERT"), []byte("KEY"), []byte("CA")
	if err := SaveCertMaterial(home, cert, key, ca); err != nil {
		t.Fatal(err)
	}
	if !HasCertMaterial(home) {
		t.Fatal("HasCertMaterial = false after save")
	}

	for _, name := range []string{CertFileName, KeyFileName, CAFileName} {
		info, err := os.Stat(home + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != Mode {
			t.Errorf("%s mode = %o, want %o", name, perm, Mode)
		}
	}

	gotCert, gotKey, gotCA, err := LoadCertMaterial(home)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != "CERT" || string(gotKey) != "KEY" || string(gotCA) != "CA" {
		t.Errorf("loaded cert=%s key=%s ca=%s", gotCert, gotKey, gotCA)
	}
}

func TestDeleteAlsoRemovesCertMaterial(t *testing.T) {
	home := t.TempDir()
	if err := Save(home, Credential{Host: "h", AgentToken: "t", AgentEndpoint: "agents.example:8443"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveCertMaterial(home, []byte("c"), []byte("k"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := Delete(home); err != nil {
		t.Fatal(err)
	}
	if HasCertMaterial(home) {
		t.Error("Delete must remove the mTLS material too")
	}
}
