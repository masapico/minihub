//go:build !windows

package backend

import (
	"errors"
	"os"
	"path/filepath"
)

func publishFile(source, target string, replace bool) error {
	if replace {
		if err := os.Rename(source, target); err != nil {
			return err
		}
	} else {
		// Link provides atomic no-replace publication on the same filesystem.
		if err := os.Link(source, target); err != nil {
			return err
		}
		if err := os.Remove(source); err != nil {
			return err
		}
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
