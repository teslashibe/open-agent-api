//go:build !windows

package auth

import "os"

var renameAuthFile = os.Rename

// secureCheckpoint makes the checkpoint private before credentials are written.
func secureCheckpoint(temp *os.File, _ string) error { return temp.Chmod(0o600) }

// syncAuthDirectory makes a renamed profile entry durable across power loss.
func syncAuthDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
