//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package session

import "os"

// Do not confuse Unix-style mode bits with effective Windows ACL protection.
func validateStorageDirectory(string, bool) error { return ErrPrivacyUnsupported }
func validatePrivateFile(os.FileInfo) error       { return ErrPrivacyUnsupported }
func openPrivateFile(string) (*os.File, error)    { return nil, ErrPrivacyUnsupported }
