package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Profile locks coordinate Sources in this process, not the independently
// running Codex CLI. Profile paths are fixed for a Source's lifetime.
var authProfileLocks sync.Map

func absoluteAuthPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return filepath.Clean(path)
}

func lockAuthProfile(path string) func() {
	key := absoluteAuthPath(path)
	if resolved, err := filepath.EvalSymlinks(key); err == nil {
		key = resolved
	}
	lock, _ := authProfileLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// replaceAuthFile uses the same private temporary-file and synced replacement
// pattern as the Codex load-history checkpoint. A failed write leaves the old
// file intact. On Windows the checkpoint keeps the replaced file's owner and
// protected DACL. The snapshot check rejects external changes already observed
// before replacement; it is not a cross-process compare-and-swap with the CLI.
func replaceAuthFile(path string, original, updated []byte, replace func(string, string) error) error {
	return replaceAuthFileChecked(path, original, updated, replace, syncAuthDirectory, nil)
}

// A checked (lifetime renewal) replacement fails unless its directory entry is
// durable too. Ordinary refresh keeps working where directories cannot sync.
func replaceAuthFileChecked(path string, original, updated []byte, replace func(string, string) error, syncDir func(string) error, checkProfile func() error) error {
	if checkProfile != nil {
		if err := checkProfile(); err != nil {
			return err
		}
	}
	// Auth paths may be symlinks. Replace the target, preserving the link.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return errors.New("persist codex auth: resolve profile failed")
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".codex-auth-*.tmp")
	if err != nil {
		return errors.New("persist codex auth: create checkpoint failed")
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	defer temp.Close()
	if err := secureCheckpoint(temp, target); err != nil {
		return errors.New("persist codex auth: secure checkpoint failed")
	}
	if _, err := temp.Write(updated); err != nil {
		return errors.New("persist codex auth: write checkpoint failed")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("persist codex auth: sync checkpoint failed")
	}
	if err := temp.Close(); err != nil {
		return errors.New("persist codex auth: close checkpoint failed")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, original) {
		return errors.New("persist codex auth: profile changed during refresh")
	}
	currentTarget, err := filepath.EvalSymlinks(path)
	if err != nil || currentTarget != target {
		return errors.New("persist codex auth: profile target changed during refresh")
	}
	if checkProfile != nil {
		if err := checkProfile(); err != nil {
			return err
		}
	}
	if err := replace(tempName, target); err != nil {
		return errors.New("persist codex auth: replace checkpoint failed")
	}
	// Without this, power loss can restore a refresh token already consumed.
	if err := syncDir(filepath.Dir(target)); err != nil && checkProfile != nil {
		return errors.New("persist codex auth: sync profile directory failed")
	}
	return nil
}
