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
