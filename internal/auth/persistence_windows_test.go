package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Mirrors a profile created by an owner that requires a protected private ACL:
// an explicit owner and DACL for the user and SYSTEM, under a directory whose
// ACL would otherwise be inherited by a new checkpoint.
func protectedWindowsProfile(t *testing.T, user string, contents []byte) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "profile")
	create := func(path, sddl string, directory bool) {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		if directory {
			if err := windows.CreateDirectory(name, &sa); err != nil {
				t.Fatal(err)
			}
			return
		}
		h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		f := os.NewFile(uintptr(h), path)
		_, err = f.Write(contents)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	create(home, "O:"+user+"D:P(A;OICI;FA;;;"+user+")(A;OICI;FA;;;SY)", true)
	path := filepath.Join(home, "auth.json")
	create(path, "O:"+user+"D:P(A;;FA;;;"+user+")(A;;FA;;;SY)", false)
	return path
}

func assertProtectedPrivateWindowsFile(t *testing.T, path string, user *windows.SID) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		t.Fatal("profile owner changed")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("profile DACL is no longer protected")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		t.Fatal("profile DACL changed")
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			t.Fatal("profile DACL gained inherited or unexpected entries")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user) && !sid.Equals(system) {
			t.Fatal("profile DACL grants another identity")
		}
	}
}

func TestAuthRenewalKeepsProtectedWindowsProfileSecurity(t *testing.T) {
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	user := token.User.Sid
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			now := time.Unix(2000000000, 0)
			contents, err := json.Marshal(map[string]any{"tokens": map[string]any{
				"access_token": lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(30*time.Second).Unix())),
				"account_id":   "synthetic-private-account", "refresh_token": "synthetic-private-refresh",
			}})
			if err != nil {
				t.Fatal(err)
			}
			path := protectedWindowsProfile(t, user.String(), contents)
			assertProtectedPrivateWindowsFile(t, path, user)
			server, calls := lifetimeServer(t, lifetimeJWT(fmt.Sprintf(`{"exp":%d}`, now.Add(time.Hour).Unix())), 3600, nil)
			s := syntheticSource(path, server)
			s.now = func() time.Time { return now }
			if strict {
				err = s.EnsureValidUntil(context.Background(), now.Add(150*time.Second))
			} else {
				_, err = s.Get(context.Background())
			}
			if err != nil || calls.Load() != 1 {
				t.Fatal("protected profile did not renew", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(data, []byte("synthetic-rotated-refresh")) {
				t.Fatal("renewal was not persisted")
			}
			assertProtectedPrivateWindowsFile(t, path, user)
			assertNoAuthCheckpoints(t, path)
		})
	}
}
