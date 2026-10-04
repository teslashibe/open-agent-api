package auth

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// renameAuthFile replaces the profile with write-through. Non-administrators
// cannot sync a Windows directory, so this is what makes the rename durable.
func renameAuthFile(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func syncAuthDirectory(string) error { return nil }

// secureCheckpoint gives the checkpoint the replaced profile's owner and, when
// protected, its DACL. A new file otherwise inherits the directory ACL, rename
// keeps that descriptor, and owners that require a protected private profile
// would reject it. An unprotected profile inherits from the same directory.
func secureCheckpoint(temp *os.File, target string) error {
	original, err := fileSecurity(target)
	if err != nil {
		return err
	}
	owner, _, err := original.Owner()
	if err != nil || owner == nil {
		return errors.New("profile owner is unavailable")
	}
	control, _, err := original.Control()
	if err != nil {
		return err
	}
	protected := control&windows.SE_DACL_PROTECTED != 0
	current, err := windows.GetSecurityInfo(windows.Handle(temp.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	currentOwner, _, err := current.Owner()
	if err != nil || currentOwner == nil {
		return errors.New("checkpoint owner is unavailable")
	}
	var info windows.SECURITY_INFORMATION
	var access uint32
	if !currentOwner.Equals(owner) {
		info |= windows.OWNER_SECURITY_INFORMATION
		access |= windows.WRITE_OWNER
	}
	var dacl *windows.ACL
	if protected {
		if dacl, _, err = original.DACL(); err != nil {
			return err
		}
		info |= windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
		access |= windows.WRITE_DAC
	}
	if info != 0 {
		h, err := openSameFile(temp, access)
		if err != nil {
			return err
		}
		err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
	}
	applied, err := windows.GetSecurityInfo(windows.Handle(temp.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	appliedOwner, _, err := applied.Owner()
	if err != nil || appliedOwner == nil || !appliedOwner.Equals(owner) {
		return errors.New("checkpoint owner differs from profile")
	}
	if appliedControl, _, err := applied.Control(); err != nil || (appliedControl&windows.SE_DACL_PROTECTED != 0) != protected {
		return errors.New("checkpoint ACL protection differs from profile")
	}
	return nil
}

// fileSecurity reads a file's owner, DACL and control bits without following a
// reparse point, so it describes the entry that rename will replace.
func fileSecurity(path string) (*windows.SECURITY_DESCRIPTOR, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	return windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
}

// openSameFile opens the checkpoint again with security-write access, which the
// os.File handle lacks, and refuses a different file found at its name.
func openSameFile(f *os.File, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, err
	}
	var want, got windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &want) != nil || windows.GetFileInformationByHandle(h, &got) != nil ||
		want.VolumeSerialNumber != got.VolumeSerialNumber || want.FileIndexHigh != got.FileIndexHigh || want.FileIndexLow != got.FileIndexLow {
		windows.CloseHandle(h)
		return 0, errors.New("checkpoint identity changed")
	}
	return h, nil
}
