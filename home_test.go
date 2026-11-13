package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheHomeIsMadeInsideTheVolumeAndIsOursAlone(t *testing.T) {
	vol := t.TempDir()
	home := filepath.Join(vol, "hermes")
	if err := prepareHome(home); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(home)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("home: %v %v", st, err)
	}
	if err := prepareHome(home); err != nil {
		t.Fatalf("a second time: %v", err)
	}
}

func TestAnInstallInTheVolumesRootIsMovedIntoTheHome(t *testing.T) {
	vol := t.TempDir()
	// The old layout: Hermes' files and ours in the volume's root.
	_ = os.MkdirAll(filepath.Join(vol, ".shpyrd"), 0o755)
	_ = os.WriteFile(filepath.Join(vol, setupFile), []byte(`{"provider":"openrouter","model":"m"}`), 0o644)
	_ = os.WriteFile(filepath.Join(vol, "config.yaml"), []byte("model:\n"), 0o644)
	_ = os.WriteFile(filepath.Join(vol, ".env"), []byte("A=1\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(vol, "sessions", "x"), 0o755)
	_ = os.MkdirAll(filepath.Join(vol, "lost+found"), 0o700)
	home := filepath.Join(vol, "hermes")
	if err := prepareHome(home); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{setupFile, "config.yaml", ".env", "sessions/x"} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			t.Errorf("%s was not moved: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(vol, f)); err == nil {
			t.Errorf("%s is still in the root", f)
		}
	}
	if _, err := os.Stat(filepath.Join(vol, "lost+found")); err != nil {
		t.Error("lost+found must stay")
	}
	if err := prepareHome(home); err != nil {
		t.Fatalf("a second time: %v", err)
	}
}
