//go:build windows

package config

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Access rights that let someone read the file's content.
const readAccess = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL | windows.FILE_GENERIC_READ

// trusted returns the SIDs that may access the config: the current user and
// LocalSystem (which can read everything anyway).
func trusted() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("current user: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	return []*windows.SID{user.User.Sid, system}, nil
}

// protect replaces the DACL of path with one that grants full access to
// the trusted SIDs only, and protects it from inheriting the parent's
// entries (which is how a file in a shared directory would leak).
func protect(path string, inherit uint32) error {
	sids, err := trusted()
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, len(sids))
	for i, sid := range sids {
		entries[i] = windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func makePrivateFile(f *os.File) error { return protect(f.Name(), windows.NO_INHERITANCE) }

func makePrivateDir(dir string) error {
	return protect(dir, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

// checkPrivate refuses a file whose DACL lets anyone but the trusted SIDs
// read it. A missing (null) DACL grants everyone full access.
func checkPrivate(path string, _ fs.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read the permissions of %s: %w", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return &PermissionError{Path: path, Others: "everyone (no access control list)"}
	}
	sids, err := trusted()
	if err != nil {
		return err
	}
	var others []string
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read the permissions of %s: %w", path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Mask&readAccess == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- the SID is stored inline in the ACE
		ok := false
		for _, t := range sids {
			if sid.Equals(t) {
				ok = true
				break
			}
		}
		if !ok {
			others = append(others, accountName(sid))
		}
	}
	if len(others) > 0 {
		return &PermissionError{Path: path, Others: strings.Join(others, ", ")}
	}
	return nil
}

func accountName(sid *windows.SID) string {
	if account, domain, _, err := sid.LookupAccount(""); err == nil {
		if domain != "" {
			return domain + `\` + account
		}
		return account
	}
	return sid.String()
}
