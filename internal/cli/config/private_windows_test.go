//go:build windows

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// grantEveryoneRead adds a read entry for Everyone to path.
func grantEveryoneRead(t *testing.T, path string) {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	old, _, _ := sd.DACL()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone)},
	}}, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

// TestWindowsConfigIsPrivate covers R-010: a config in a directory others
// can read is still saved privately, and a readable config is refused.
func TestWindowsConfigIsPrivate(t *testing.T) {
	shared := t.TempDir()
	grantEveryoneRead(t, shared)
	path := filepath.Join(shared, "config.toml")
	f := &File{DefaultProfile: "main", Profiles: map[string]*Profile{"main": {URL: "https://x", Key: "vela_k_secret"}}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("a freshly saved config is not private: %v", err)
	}
	grantEveryoneRead(t, path)
	_, err := Load(path)
	var pe *PermissionError
	if !errors.As(err, &pe) || pe.Others == "" {
		t.Fatalf("Load of a readable config = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A directory created for the config is private as well.
	nested := filepath.Join(shared, "new", "config.toml")
	if err := f.Save(nested); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(filepath.Dir(nested), nil); err != nil {
		t.Fatalf("created directory is not private: %v", err)
	}
}
