package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

type authProfileIdentity struct {
	parent, file os.FileInfo
}

// Explicit lifetime renewal requires a direct private profile. The node owns
// Windows directory ACL validation; Unix permission checks are also enforceable
// here. Identity checks reject observed replacements, not an adversarial swap
// between the final check and rename or an independently running CLI writer.
func inspectAuthProfile(path string) (authProfileIdentity, error) {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && parent.Mode().Perm()&0077 != 0) {
		return authProfileIdentity{}, errors.New("codex authentication profile directory is unavailable")
	}
	file, err := os.Lstat(path)
	if err != nil || !file.Mode().IsRegular() || (runtime.GOOS != "windows" && file.Mode().Perm()&0077 != 0) {
		return authProfileIdentity{}, errors.New("codex authentication profile file is unavailable")
	}
	// Windows FileInfo can defer loading its file ID until SameFile. Resolve
	// both IDs before OAuth, while these paths still describe this snapshot.
	if !os.SameFile(parent, parent) || !os.SameFile(file, file) {
		return authProfileIdentity{}, errors.New("codex authentication profile identity is unavailable")
	}
	return authProfileIdentity{parent, file}, nil
}

func (id authProfileIdentity) check(path string) error {
	current, err := inspectAuthProfile(path)
	if err != nil || !os.SameFile(id.parent, current.parent) || !os.SameFile(id.file, current.file) {
		return errors.New("codex authentication profile changed during renewal")
	}
	return nil
}
