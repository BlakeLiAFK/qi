// Package testutil provides private filesystem fixtures for persistence tests.
package testutil

import "os"

type TempDirectoryTest interface {
	Helper()
	TempDir() string
	Fatal(...any)
}

// PrivateDir explicitly tightens a newly created test-owned fixture, never user
// data. testing.T.TempDir's numbered child otherwise follows the process umask.
func PrivateDir(t TempDirectoryTest) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
