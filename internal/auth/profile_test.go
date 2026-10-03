package auth

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestEnsureValidUntilRejectsIdenticalProfileReplacementOrRemoval(t *testing.T) {
	for _, change := range []string{"file-replaced", "parent-replaced", "file-removed", "parent-removed"} {
		t.Run(change, func(t *testing.T) {
			now := time.Unix(2000000000, 0)
			root := t.TempDir()
			home := filepath.Join(root, "profile")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "auth.json")
			writeJSON(t, path, map[string]any{"tokens": map[string]any{
				"access_token": lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(90*time.Second).Unix())),
				"account_id":   "synthetic-private-account", "refresh_token": "synthetic-private-refresh",
			}})
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			server, calls := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, func() {
				var err error
				switch change {
				case "file-replaced":
					candidate := filepath.Join(home, "replacement.json")
					err = os.WriteFile(candidate, original, 0600)
					if err == nil {
						err = os.Rename(candidate, path)
					}
				case "parent-replaced":
					err = os.Rename(home, filepath.Join(root, "removed-profile"))
					if err == nil {
						err = os.Mkdir(home, 0700)
					}
					if err == nil {
						err = os.WriteFile(path, original, 0600)
					}
				case "file-removed":
					err = os.Remove(path)
				case "parent-removed":
					err = os.Rename(home, filepath.Join(root, "removed-profile"))
				}
				if err != nil {
					t.Error(err)
				}
			})
			s := syntheticSource(path, server)
			s.now = func() time.Time { return now }
			if err := s.EnsureValidUntil(context.Background(), now.Add(150*time.Second)); err == nil || calls.Load() != 1 || s.cache != (Credentials{}) {
				t.Fatal("changed profile identity accepted renewal")
			}
			if change == "file-replaced" || change == "parent-replaced" {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, original) {
					t.Fatal("renewal overwrote the identical replacement profile")
				}
				assertNoAuthCheckpoints(t, path)
			} else if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("renewal recreated a removed profile")
			}
		})
	}
}

func TestAuthOnlyPersistenceRechecksIdentityAtPublicationBoundary(t *testing.T) {
	path := lifetimeProfile(t, time.Now().Add(time.Hour), false)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := inspectAuthProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	err = replaceAuthFileChecked(path, original, []byte(`{"synthetic":"must-not-publish"}`), os.Rename, func() error {
		checks++
		if checks == 2 {
			candidate := filepath.Join(filepath.Dir(path), "replacement.json")
			if err := os.WriteFile(candidate, original, 0600); err != nil {
				return err
			}
			if err := os.Rename(candidate, path); err != nil {
				return err
			}
		}
		return identity.check(path)
	})
	if err == nil || checks != 2 {
		t.Fatal("publication did not reject the identical replacement inode")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("publication changed the replaced profile")
	}
	assertNoAuthCheckpoints(t, path)
}

func TestEnsureValidUntilRequiresDirectPrivateProfile(t *testing.T) {
	for _, kind := range []string{"file-symlink", "parent-symlink", "nonregular", "shared-file", "shared-parent"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && (kind == "shared-file" || kind == "shared-parent") {
				t.Skip("Windows ACL validation belongs to the profile owner")
			}
			now := time.Unix(2000000000, 0)
			path := lifetimeProfile(t, now.Add(90*time.Second), true)
			switch kind {
			case "file-symlink":
				target := path + ".target"
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skip("synthetic symlinks unavailable")
				}
			case "parent-symlink":
				link := filepath.Join(t.TempDir(), "linked-profile")
				if err := os.Symlink(filepath.Dir(path), link); err != nil {
					t.Skip("synthetic symlinks unavailable")
				}
				path = filepath.Join(link, "auth.json")
			case "nonregular":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "shared-file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "shared-parent":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			}
			server, calls := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, nil)
			s := syntheticSource(path, server)
			s.now = func() time.Time { return now }
			if err := s.EnsureValidUntil(context.Background(), now.Add(150*time.Second)); err == nil || calls.Load() != 0 {
				t.Fatal("indirect, nonregular or unowned profile started OAuth renewal")
			}
		})
	}
}
