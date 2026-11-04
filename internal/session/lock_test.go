package session

import (
	"errors"
	"testing"
)

func TestEngineLockExclusive(t *testing.T) {
	store, path := openTest(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	release, err := store.AcquireEngineLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, candidate := range []*Store{store, other} {
		if extra, err := candidate.AcquireEngineLock(); !errors.Is(err, ErrEngineLocked) {
			if extra != nil {
				extra()
			}
			t.Fatalf("second owner admitted: %v", err)
		}
	}
	release()
	release()
	next, err := other.AcquireEngineLock()
	if err != nil {
		t.Fatalf("did not release: %v", err)
	}
	defer next()
}
