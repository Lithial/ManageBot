package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// LocateBinary returns the absolute path to a binary in the repo's bin/
// directory. It walks up from the test file's location to find the repo
// root (identified by the presence of go.mod), then returns repoRoot/bin/<name>.
// Returns an error if the binary doesn't exist; the integration-test Makefile
// target is responsible for building all binaries before tests run.
func LocateBinary(name string) (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot determine caller for LocateBinary")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			path := filepath.Join(dir, "bin", name)
			if _, err := os.Stat(path); err != nil {
				return "", err
			}
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}
