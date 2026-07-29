//go:build windows

package fsperm

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureDir(path string) error {
	// The mode argument is ignored by the Windows syscall layer. MkdirAll is
	// still the right call -- it creates the tree -- but it supplies exactly no
	// protection here, which is the entire reason the next line exists.
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return restrict(path, true)
}

func secureFile(path string) error { return restrict(path, false) }

// restrict replaces the object's DACL with one granting full control to the
// object's owner and to SYSTEM, and to nobody else.
//
// Two details carry the whole guarantee:
//
//   - PROTECTED_DACL_SECURITY_INFORMATION detaches the object from its parent's
//     inheritable ACEs. Without it the new grants are ADDED to whatever was
//     inherited, and the inherited set under the default ACL on a system drive
//     includes BUILTIN\Users. Granting the owner access while leaving Users in
//     place protects nothing, and looks correct in a code review.
//
//   - Directories are marked inheritable, so files created inside LATER are
//     covered. That is required rather than tidy: autocert writes the ACME
//     account key through its own code path that we never see, so inheritance
//     is the only mechanism that can protect it.
func restrict(path string, container bool) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read owner of %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("owner of %s: %w", path, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}

	inherit := uint32(windows.NO_INHERITANCE)
	if container {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{grantFull(owner, windows.TRUSTEE_IS_USER, inherit)}
	// Running as a service means the owner ALREADY is SYSTEM. A second identical
	// ACE would be harmless but makes the ACL confusing to anyone auditing it
	// with icacls, which is exactly when clarity matters.
	if !owner.Equals(system) {
		entries = append(entries, grantFull(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inherit))
	}

	// nil merged ACL: the new list REPLACES rather than extends. Passing the
	// existing DACL here would preserve the Users ACE we are removing.
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build DACL for %s: %w", path, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	return nil
}

// checkPrivate walks the object's DACL and reports the first entry granting
// access to anything other than the owner or SYSTEM.
//
// Reads the SECURITY DESCRIPTOR rather than shelling out to icacls, which
// prints LOCALISED group names -- "Jeder" on a German host, "Users" on an
// English one. SIDs are identical on every install.
func checkPrivate(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read security info for %s: %w", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("owner of %s: %w", path, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve SYSTEM sid: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("DACL of %s: %w", path, err)
	}
	// A NULL DACL is not "nothing is permitted", it is "everything, to
	// everyone". Reading it as an empty ACE list gets the answer exactly
	// backwards, so it is rejected explicitly.
	if dacl == nil {
		return fmt.Errorf("%s has a NULL DACL, which grants full access to every account", path)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read ACE %d of %s: %w", i, path, err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(owner) || sid.Equals(system) {
			continue
		}
		return fmt.Errorf("%s grants access to %s, which is neither its owner (%s) nor SYSTEM",
			path, sid, owner)
	}
	return nil
}

func grantFull(sid *windows.SID, typ windows.TRUSTEE_TYPE, inherit uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  typ,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
