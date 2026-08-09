package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectPrefersContainerOverSystemd(t *testing.T) {
	// A container can also run systemd inside it, and being in a container is
	// the stronger fact: whatever supervises the process, the IMAGE still
	// decides what comes back after a recreate.
	env := func(string) string { return "some-invocation-id" }
	exists := func(p string) bool { return p == "/.dockerenv" }
	if got := Detect(env, exists); got != MethodDocker {
		t.Errorf("Detect = %q, want %q: a container with systemd inside is still a container", got, MethodDocker)
	}
}

func TestDetect(t *testing.T) {
	none := func(string) bool { return false }
	for _, tc := range []struct {
		name   string
		env    string
		exists func(string) bool
		want   Method
	}{
		{"docker", "", func(p string) bool { return p == "/.dockerenv" }, MethodDocker},
		{"podman", "", func(p string) bool { return p == "/run/.containerenv" }, MethodDocker},
		{"systemd", "abc123", none, MethodSystemd},
		{"bare binary", "", none, MethodManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(func(string) string { return tc.env }, tc.exists)
			if got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// Only systemd may act. The others must produce a COMMAND, because acting on
// them is either useless (docker) or reckless (unknown supervisor).
func TestOnlySystemdUpgradesItself(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "polyemesis")
	os.WriteFile(bin, []byte("binary"), 0o755)

	for _, tc := range []struct {
		m         Method
		automatic bool
	}{
		{MethodDocker, false},
		{MethodManual, false},
		{MethodSystemd, true},
	} {
		p := PlanFor(tc.m, bin, "v0.6.0")
		if p.Automatic != tc.automatic {
			t.Errorf("%s: Automatic = %v, want %v", tc.m, p.Automatic, tc.automatic)
		}
		if !tc.automatic && p.Command == "" {
			t.Errorf("%s: no command offered, so an operator is told there is an update and not how to take it", tc.m)
		}
		if tc.automatic && p.BinaryPath == "" {
			t.Errorf("%s: automatic but no binary path", tc.m)
		}
	}
}

// The docker command must recreate, not merely pull. An operator who runs only
// `docker pull` has changed nothing and will reasonably believe otherwise.
func TestDockerCommandRecreatesRatherThanOnlyPulling(t *testing.T) {
	p := PlanFor(MethodDocker, "/usr/local/bin/polyemesis", "v0.6.0")
	if !strings.Contains(p.Command, "up -d") {
		t.Errorf("the docker command does not recreate the container: %q", p.Command)
	}
}

func TestSystemdRefusesAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write anywhere")
	}
	p := PlanFor(MethodSystemd, "/proc/definitely-not-writable/polyemesis", "v0.6.0")
	if p.Automatic {
		t.Error("offered an automatic upgrade into a directory it cannot write; it would fail half way")
	}
	if p.Reason == "" {
		t.Error("refused without saying why")
	}
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "artefact")
	os.WriteFile(f, []byte("hello"), 0o644)
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	if err := Verify(f, want); err != nil {
		t.Errorf("Verify on a good file: %v", err)
	}
	if err := Verify(f, strings.Repeat("0", 64)); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("Verify on a bad hash = %v, want ErrChecksumMismatch", err)
	}
	// The one that matters: no checksum must never mean "install it anyway".
	if err := Verify(f, ""); err == nil {
		t.Error("Verify accepted an empty checksum; an unverified binary would be installed " +
			"on a box with a public ingest port")
	}
}

func TestChecksumForMatchesOnBasename(t *testing.T) {
	sums := "aaa  dist/polyemesis-v0.6.0-linux-amd64\nbbb  dist/polyemesis-v0.6.0-darwin-arm64\n"
	got, err := ChecksumFor(sums, "/tmp/polyemesis-v0.6.0-linux-amd64")
	if err != nil || got != "aaa" {
		t.Errorf("ChecksumFor = %q, %v; want aaa", got, err)
	}
	if _, err := ChecksumFor(sums, "polyemesis-v0.6.0-windows-amd64.exe"); err == nil {
		t.Error("found a checksum for an artefact that is not listed")
	}
}

// A bad download must leave a RUNNING INSTALL COMPLETELY UNTOUCHED.
func TestStageLeavesTheInstallAloneWhenVerificationFails(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("the running binary"), 0o755)
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("a corrupted download"), 0o644)

	if err := Stage(bin, staged, strings.Repeat("0", 64)); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Stage = %v, want ErrChecksumMismatch", err)
	}
	got, _ := os.ReadFile(bin)
	if string(got) != "the running binary" {
		t.Error("a failed verification replaced the live binary")
	}
	if _, err := os.Stat(PreviousPath(bin)); err == nil {
		t.Error("a failed verification still set a rollback point, so a later rollback " +
			"would restore something that was never running")
	}
}

func TestStageThenRollbackRoundTrips(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("old"), 0o755)
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("new"), 0o644)
	h := hashOf(t, staged)
	if err := Stage(bin, staged, h); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new" {
		t.Fatalf("binary is %q, want new", b)
	}
	if b, _ := os.ReadFile(PreviousPath(bin)); string(b) != "old" {
		t.Fatalf("previous is %q, want old", b)
	}

	if err := Rollback(bin); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "old" {
		t.Errorf("after rollback the binary is %q, want old", b)
	}
	// A rollback taken by mistake must be undoable without the release page.
	if b, _ := os.ReadFile(PreviousPath(bin)); string(b) != "new" {
		t.Errorf("after rollback the previous is %q, want new — a second rollback cannot undo the first", b)
	}
	if err := Rollback(bin); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new" {
		t.Errorf("a second rollback did not return to %q", "new")
	}
}

func TestRollbackWithNothingStaged(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "polyemesis")
	os.WriteFile(bin, []byte("only ever this"), 0o755)
	if err := Rollback(bin); err == nil {
		t.Error("Rollback succeeded with no previous binary")
	}
}

func hashOf(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The backup is owner-only; the live binary is not.
//
// Only the thing at the binary path needs to be executable by whoever runs the
// service. A backup beside it is read by exactly one thing -- a rollback,
// running as the same user -- so world read and execute on it buys nothing and
// widens what a local account can reach. Sonar's S2612 caught this.
func TestTheBackupIsOwnerOnlyAndTheLiveBinaryIsNot(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix mode bits: os.Chmod honours only the read-only
		// flag and every file reports 0666 whatever was asked for. Asserting
		// 0700 against 0755 there tests the operating system rather than this
		// package, and it fails for a reason that says nothing about the code.
		t.Skip("Unix permission bits")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("old"), 0o755)
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("new"), 0o644)

	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	prev, err := os.Stat(PreviousPath(bin))
	if err != nil {
		t.Fatalf("stat previous: %v", err)
	}
	if m := prev.Mode().Perm(); m&0o077 != 0 {
		t.Errorf("the backup is mode %o; it must not be readable or executable by "+
			"group or other, because nothing but a same-user rollback ever reads it", m)
	}

	live, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}
	if m := live.Mode().Perm(); m&0o111 == 0 {
		t.Errorf("the live binary is mode %o and not executable; the service could "+
			"not start it", m)
	}
}

// And a rollback has to put the live mode back, since the file it promotes was
// being kept owner-only.
func TestRollbackRestoresTheExecutableMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix mode bits: os.Chmod honours only the read-only
		// flag and every file reports 0666 whatever was asked for. Asserting
		// 0700 against 0755 there tests the operating system rather than this
		// package, and it fails for a reason that says nothing about the code.
		t.Skip("Unix permission bits")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("old"), 0o755)
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("new"), 0o644)

	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := Rollback(bin); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if m := st.Mode().Perm(); m&0o111 == 0 {
		t.Errorf("after a rollback the binary is mode %o and not executable -- the "+
			"service would fail to start on the version being rolled back TO, which "+
			"is the one moment that must work", m)
	}
}

// An upgrade must not WIDEN the mode an operator chose.
//
// Hardcoding 0o755 meant an install deliberately locked to 0o750 came back
// world-executable after the first upgrade. The live file is the authority on
// what this install uses.
func TestAnUpgradePreservesTheInstalledMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix mode bits: os.Chmod honours only the read-only
		// flag and every file reports 0666 whatever was asked for. Asserting
		// 0700 against 0755 there tests the operating system rather than this
		// package, and it fails for a reason that says nothing about the code.
		t.Skip("Unix permission bits")
	}
	for _, mode := range []os.FileMode{0o755, 0o750, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "polyemesis")
			os.WriteFile(bin, []byte("old"), 0o644)
			if err := os.Chmod(bin, mode); err != nil {
				t.Fatal(err)
			}
			staged := filepath.Join(dir, "staged")
			os.WriteFile(staged, []byte("new"), 0o644)

			if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
				t.Fatalf("Stage: %v", err)
			}
			st, _ := os.Stat(bin)
			if got := st.Mode().Perm(); got != mode {
				t.Errorf("after an upgrade the binary is %o, want the installed %o -- "+
					"an upgrade must not change who can run it", got, mode)
			}
		})
	}
}

// A live file that is somehow not executable must not produce a replacement
// that is also not executable: the install is already broken, and refusing to
// make the new binary runnable turns that into an outage.
func TestAnUnexecutableInstallStillYieldsARunnableBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix mode bits: os.Chmod honours only the read-only
		// flag and every file reports 0666 whatever was asked for. Asserting
		// 0700 against 0755 there tests the operating system rather than this
		// package, and it fails for a reason that says nothing about the code.
		t.Skip("Unix permission bits")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("old"), 0o600)
	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("new"), 0o644)

	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	st, _ := os.Stat(bin)
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary is %o and not owner-executable; the service could not start it",
			st.Mode().Perm())
	}
}
