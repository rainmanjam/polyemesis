package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareSetupCodeMintsAGroupedCodeAndWritesItOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	c, err := PrepareSetupCode(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Z2-9]{4}(-[A-Z2-9]{4}){3}$`).MatchString(c.Code()) {
		t.Fatalf("code %q is not four groups of four", c.Code())
	}
	if !c.Pending() || c.Reused() || c.Preset() {
		t.Fatalf("fresh code: pending=%v reused=%v preset=%v", c.Pending(), c.Reused(), c.Preset())
	}
	path := filepath.Join(dir, SetupCodeFile)
	if c.Path() != path {
		t.Fatalf("path = %q, want %q", c.Path(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != c.Code() {
		t.Fatalf("file holds %q (err %v), want %q", b, err, c.Code())
	}
	// Windows has no Unix mode bits to assert; the owner-only claim there is
	// the data directory's ACL, which this package does not set.
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestPrepareSetupCodeTightensALeftoverFilesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SetupCodeFile)
	if err := os.WriteFile(path, []byte("ABCD-EFGH-JKMN-PQRS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSetupCode(dir, false, ""); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("a leftover 0644 file stayed %v; the code is readable by every account", fi.Mode().Perm())
		}
	}
}

// A restart before setup must not invalidate the code the operator already
// copied out of the log.
func TestPrepareSetupCodeKeepsTheCodeAcrossARestart(t *testing.T) {
	dir := t.TempDir()
	first, err := PrepareSetupCode(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareSetupCode(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Code() != first.Code() || !second.Reused() {
		t.Fatalf("restart gave %q (reused=%v), want %q kept", second.Code(), second.Reused(), first.Code())
	}
	if !second.Matches(first.Code()) {
		t.Fatal("the first boot's code does not match after a restart")
	}
}

func TestPrepareSetupCodeReplacesAFileTooShortToBeACode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SetupCodeFile), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := PrepareSetupCode(dir, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Reused() || c.Matches("x") {
		t.Fatal("a one-character file was accepted as the setup code")
	}
}

func TestPrepareSetupCodeUsesAPresetAndRefusesAShortOne(t *testing.T) {
	dir := t.TempDir()
	c, err := PrepareSetupCode(dir, false, "readable-placeholder-code")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Preset() || !c.Matches("readable-placeholder-code") {
		t.Fatalf("preset not used: preset=%v code=%q", c.Preset(), c.Code())
	}
	b, _ := os.ReadFile(filepath.Join(dir, SetupCodeFile))
	if strings.TrimSpace(string(b)) != "readable-placeholder-code" {
		t.Fatalf("file holds %q, want the preset", b)
	}

	if _, err := PrepareSetupCode(t.TempDir(), false, "short"); err == nil ||
		!strings.Contains(err.Error(), SetupCodeEnv) {
		t.Fatalf("a five-character preset was accepted (err %v)", err)
	}
}

func TestPrepareSetupCodeRemovesALeftoverOnceAnAdminExists(t *testing.T) {
	dir := t.TempDir()
	if _, err := PrepareSetupCode(dir, false, ""); err != nil {
		t.Fatal(err)
	}
	c, err := PrepareSetupCode(dir, true, "")
	if err != nil || c != nil {
		t.Fatalf("with a user: code %v, err %v; want nil, nil", c, err)
	}
	if _, err := os.Stat(filepath.Join(dir, SetupCodeFile)); !os.IsNotExist(err) {
		t.Fatalf("leftover setup code survived a boot with an admin (stat err %v)", err)
	}
}

func TestPrepareSetupCodeReportsAnUnwritableDataDir(t *testing.T) {
	// A regular file where the data directory should be: nothing can be
	// created under it on any platform.
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSetupCode(notDir, false, ""); err == nil {
		t.Fatal("no error writing a setup code under a regular file")
	}
	if _, err := PrepareSetupCode(filepath.Join(notDir, "sub"), true, ""); err == nil {
		t.Fatal("no error removing a leftover under a regular file")
	}
}

func TestSetupCodeMatchesForgivesCaseSpacesAndDashesOnly(t *testing.T) {
	c, err := PrepareSetupCode(t.TempDir(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	code := c.Code()
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if !c.Matches(typed) {
		t.Fatalf("%q typed as %q did not match", code, typed)
	}
	for _, bad := range []string{"", code[:len(code)-1], code + "A"} {
		if c.Matches(bad) {
			t.Errorf("%q matched %q", bad, code)
		}
	}
}

func TestSetupCodeConsumeStopsMatchingAndRemovesTheFile(t *testing.T) {
	c, err := PrepareSetupCode(t.TempDir(), false, "")
	if err != nil {
		t.Fatal(err)
	}
	code := c.Code()
	if err := c.Consume(); err != nil {
		t.Fatal(err)
	}
	if c.Pending() || c.Matches(code) {
		t.Fatal("the code still works after the admin was created")
	}
	if _, err := os.Stat(c.Path()); !os.IsNotExist(err) {
		t.Fatalf("setup code file survived Consume (stat err %v)", err)
	}
	// Twice is harmless: the file is already gone.
	if err := c.Consume(); err != nil {
		t.Fatalf("second Consume: %v", err)
	}
}

// A nil code is what a server built without one holds. It must read as
// "nothing matches", never as "anything goes".
func TestNilSetupCodeFailsClosed(t *testing.T) {
	var c *SetupCode
	if c.Pending() || c.Matches("") || c.Matches("anything") || c.Reused() || c.Preset() {
		t.Fatal("a nil setup code accepted something")
	}
	if c.Code() != "" || c.Path() != "" || c.Consume() != nil {
		t.Fatal("a nil setup code reported state")
	}
}
