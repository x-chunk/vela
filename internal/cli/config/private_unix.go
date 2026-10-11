//go:build !windows

package config

import (
	"io/fs"
	"os"
)

// checkPrivate refuses a file that group or others can access.
func checkPrivate(path string, info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return &PermissionError{Path: path, Mode: info.Mode()}
	}
	return nil
}

func makePrivateFile(f *os.File) error { return f.Chmod(0o600) }

func makePrivateDir(dir string) error { return os.Chmod(dir, 0o700) }
