package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// prepareHome makes Hermes' home inside the volume. The volume's root
// belongs to root, handed to the process's group by the platform; Hermes
// refuses a home another user owns or anyone else may write to (its
// control socket lives there), so the home is a directory of our own,
// 0700. An install from before this layout kept everything in the
// volume's root: its entries are moved in, which a world-writable root
// permits, before Hermes runs.
func prepareHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("making Hermes' home: %w", err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return fmt.Errorf("Hermes' home must be ours alone: %w", err)
	}
	parent := filepath.Dir(home)
	if _, err := os.Stat(filepath.Join(parent, setupFile)); err != nil {
		return nil // nothing of the old layout
	}
	if _, err := os.Stat(filepath.Join(home, setupFile)); err == nil {
		return nil // moved already
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	own := filepath.Base(home)
	for _, e := range entries {
		if e.Name() == own || e.Name() == "lost+found" {
			continue
		}
		from, to := filepath.Join(parent, e.Name()), filepath.Join(home, e.Name())
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("moving %s into Hermes' home: %w", e.Name(), err)
		}
	}
	return nil
}
