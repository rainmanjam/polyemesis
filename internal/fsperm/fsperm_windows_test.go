//go:build windows

package fsperm

import (
	"testing"

	"golang.org/x/sys/windows"
)

func assertPrivateDir(t *testing.T, path string) {
	t.Helper()
	if err := CheckPrivate(path); err != nil {
		t.Errorf("directory is not private: %v", err)
	}
	// The DACL we set must be PROTECTED. Without that flag the new grants are
	// ADDED to the parent's inheritable ACEs rather than replacing them, and on
	// a system drive those include BUILTIN\Users. The grants would be correct,
	// present, and useless -- a failure mode CheckPrivate would catch here but
	// which is worth naming separately, because it is the one a reviewer
	// reading the DACL-building code is most likely to wave through.
	if !daclProtected(t, path) {
		t.Errorf("%s has an unprotected DACL, so it still inherits its parent's ACEs", path)
	}
}

// assertPrivateFile deliberately does NOT require a protected DACL. A file
// created inside a secured directory INHERITS its protection, and an inherited
// DACL is not itself marked protected. The resulting access is what matters.
func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	if err := CheckPrivate(path); err != nil {
		t.Errorf("file is not private: %v", err)
	}
}

func daclProtected(t *testing.T, path string) bool {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read security info for %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("control bits of %s: %v", path, err)
	}
	return control&windows.SE_DACL_PROTECTED != 0
}

// loosen grants Everyone full control, so a test that then calls Secure* is
// measuring the call rather than whatever the runner's default ACL happened to
// be. Without it the Windows tests could pass on a machine where the parent
// directory was already private for unrelated reasons.
func loosen(t *testing.T, path string) {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("resolve Everyone sid: %v", err)
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		grantFull(everyone, windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT),
	}, nil)
	if err != nil {
		t.Fatalf("build permissive DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatalf("loosen %s: %v", path, err)
	}
	// Guard the guard. If this did not actually make the object public, every
	// test that depends on it would pass without Secure* having done anything.
	if err := CheckPrivate(path); err == nil {
		t.Fatalf("loosen(%s) left the object private, so the assertion that "+
			"follows proves nothing", path)
	}
}
