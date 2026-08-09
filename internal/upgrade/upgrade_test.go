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
	"time"
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
	booted := func(p string) bool { return p == "/run/systemd/system" }
	for _, tc := range []struct {
		name   string
		env    func(string) string
		exists func(string) bool
		want   Method
	}{
		{"docker", constEnv(""), func(p string) bool { return p == "/.dockerenv" }, MethodDocker},
		{"podman", constEnv(""), func(p string) bool { return p == "/run/.containerenv" }, MethodDocker},
		{"kubernetes writes neither file", envVar("KUBERNETES_SERVICE_HOST", "10.0.0.1"), none, MethodDocker},
		{"systemd", envVar("INVOCATION_ID", "abc123"), booted, MethodSystemd},
		{"bare binary", constEnv(""), none, MethodManual},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.env, tc.exists)
			if got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

func constEnv(v string) func(string) string { return func(string) string { return v } }

func envVar(key, val string) func(string) string {
	return func(k string) string {
		if k == key {
			return val
		}
		return ""
	}
}

// INVOCATION_ID is inherited by every child of a unit. A shell spawned from
// `systemctl status`, or a binary launched by hand out of that shell, carries it
// while being supervised by nothing at all.
//
// Believing it means reporting Automatic:true -- replacing the live binary on
// the promise that a service manager will restart onto it. Nothing will, and the
// operator is left on a version they did not choose with no restart coming.
//
// THE HOST HERE IS A REAL SYSTEMD HOST. Testing this against a box with no
// systemd would be testing the easy half: /run/systemd/system exists on every
// machine this ships to, so it cannot be what separates the two cases. Only the
// cgroup can.
func TestSystemdNeedsMoreThanAnInheritedInvocationID(t *testing.T) {
	systemdHost := func(p string) bool { return p == "/run/systemd/system" }
	for _, tc := range []struct {
		name   string
		cgroup string
		want   Method
	}{
		{
			// A login session. Note "user@1000.service" in the path: the string
			// ".service" appears, which is why only the LAST component counts.
			name:   "a shell that inherited the variable",
			cgroup: "0::/user.slice/user-1000.slice/user@1000.service/session-3.scope\n",
			want:   MethodManual,
		},
		{
			name:   "systemd-run, which is a scope and not a unit we can be restarted as",
			cgroup: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/run-r123.scope\n",
			want:   MethodManual,
		},
		{
			name:   "the service itself, cgroup v2",
			cgroup: "0::/system.slice/polyemesis.service\n",
			want:   MethodSystemd,
		},
		{
			name:   "the service itself, cgroup v1",
			cgroup: "1:name=systemd:/system.slice/polyemesis.service\n2:cpu:/system.slice/polyemesis.service\n",
			want:   MethodSystemd,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withCgroup(t, tc.cgroup)
			got := Detect(envVar("INVOCATION_ID", "abc123"), systemdHost)
			if got != tc.want {
				t.Errorf("Detect = %q, want %q", got, tc.want)
			}
		})
	}
}

// A CGROUP NAMESPACE HIDES THE ANSWER, AND THAT IS NOT A "NO".
//
// A namespace reports membership relative to its own root, so a unit that has
// one -- systemd gives one to anything with PrivateMounts, and every container
// runtime sets one -- reads its own cgroup as "0::/". The last component is "/",
// which is not a ".service", so a strict reading calls a genuine supervised unit
// "manual" and takes away its upgrade path entirely.
//
// The rule is that the cgroup can prove supervision or prove a login session;
// when it says nothing, the weaker signals stand.
func TestANamespacedCgroupDoesNotDemoteAGenuineUnit(t *testing.T) {
	for _, cgroup := range []string{"0::/\n", "0::/\n1:name=systemd:/\n"} {
		t.Run(strings.ReplaceAll(cgroup, "\n", "|"), func(t *testing.T) {
			withCgroup(t, cgroup)
			got := Detect(envVar("INVOCATION_ID", "abc123"), func(p string) bool { return p == "/run/systemd/system" })
			if got != MethodSystemd {
				t.Errorf("Detect = %q, want %q: a namespaced unit is still a unit, and calling it "+
					"manual leaves a supervised install with no way to upgrade", got, MethodSystemd)
			}
		})
	}
}

// An unreadable cgroup falls back to the weaker signals rather than refusing:
// treating a genuine unit as manual leaves an operator with no upgrade path.
func TestAnUnreadableCgroupFallsBackRatherThanRefusing(t *testing.T) {
	procSelfCgroup = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { procSelfCgroup = "/proc/self/cgroup" })
	got := Detect(envVar("INVOCATION_ID", "abc123"), func(p string) bool { return p == "/run/systemd/system" })
	if got != MethodSystemd {
		t.Errorf("Detect = %q, want %q", got, MethodSystemd)
	}
}

func withCgroup(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cgroup")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	procSelfCgroup = p
	t.Cleanup(func() { procSelfCgroup = "/proc/self/cgroup" })
}

// A FAILED UPGRADE MUST NOT SPEND THE ROLLBACK POINT.
//
// Writing .previous before the new binary is in place means an install that
// fails part way -- EIO, a full disk -- has already overwritten the way back
// with a copy of the version that is still running. The operator asked to
// upgrade, the upgrade failed, and the release they could have returned to is
// gone. So the outgoing binary is copied aside first but promoted last.
//
// Forced here by handing install() an incoming file that does not exist, which
// is the one rename it can be made to fail on demand.
func TestAFailedInstallLeavesTheRollbackPointAlone(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	prev := PreviousPath(bin)
	os.WriteFile(prev, []byte("v0"), 0o700)

	installed, err := install(bin, filepath.Join(dir, ".polyemesis-incoming-vanished"), dir)
	if err == nil {
		t.Fatal("install succeeded with no incoming file")
	}
	if installed {
		t.Error("install reported the live binary was replaced when the rename failed; " +
			"the caller would delete a file it does not own")
	}
	if b, _ := os.ReadFile(prev); string(b) != "v0" {
		t.Errorf("previous is %q, want v0 — a failed upgrade spent the rollback point", b)
	}
	if b, _ := os.ReadFile(bin); string(b) != "v1" {
		t.Errorf("binary is %q, want v1", b)
	}
	assertNoLitter(t, dir, "polyemesis", "polyemesis.previous")
}

// Nothing in-process can clean up after SIGKILL, so the next run does it.
// Otherwise an install directory grows a near-copy of the binary for every
// upgrade that was interrupted.
func TestStageClearsTempFilesAKilledRunLeftBehind(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	abandoned(t, filepath.Join(dir, incomingPrefix+"123"), "half a download")
	abandoned(t, filepath.Join(dir, backupPrefix+"456"), "an orphaned backup")

	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	assertNoLitter(t, dir, "polyemesis", "polyemesis.previous", "staged")
}

// STALE, not merely present.
//
// An upgrade in flight owns temp files of exactly these names. A sweep that
// deleted every match would unlink a concurrent run's incoming binary between
// its copy and its rename, failing an upgrade that was doing nothing wrong. The
// age limit is what stands in for a lock, and a copy of a binary takes seconds.
func TestTheSweepLeavesAnotherRunsFreshTempFilesAlone(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	inFlight := filepath.Join(dir, incomingPrefix+"another-upgrade")
	os.WriteFile(inFlight, []byte("a download in progress"), 0o600)

	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := os.Stat(inFlight); err != nil {
		t.Error("swept away a temp file another upgrade was still using; that run's rename " +
			"would fail on a file it had just written correctly")
	}
}

// A directory sharing the prefix is not something this package made, and
// removing what it did not create is not its business.
func TestTheSweepDoesNotRemoveDirectories(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	notOurs := filepath.Join(dir, incomingPrefix+"a-directory")
	if err := os.Mkdir(notOurs, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(notOurs, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if _, err := os.Stat(notOurs); err != nil {
		t.Error("removed a directory it did not create")
	}
}

// abandoned writes a temp file and backdates it past the staleness threshold,
// standing in for one a killed run left behind.
func abandoned(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// AN UPGRADE MUST REPLACE WHAT THE PATH POINTS AT, NOT THE POINTER.
//
// /usr/local/bin/polyemesis pointing into /opt/polyemesis/v0.5.0/ is how release
// managers and install scripts keep versions side by side. Renaming over the
// LINK replaces that indirection with a plain file, silently dismantling the
// scheme the operator built.
func TestAnUpgradeFollowsASymlinkedBinaryToItsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows that CI does not have")
	}
	root := t.TempDir()
	releases := filepath.Join(root, "opt")
	binDir := filepath.Join(root, "bin")
	os.Mkdir(releases, 0o755)
	os.Mkdir(binDir, 0o755)

	real := filepath.Join(releases, "polyemesis-v1")
	os.WriteFile(real, []byte("v1"), 0o755)
	link := filepath.Join(binDir, "polyemesis")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	staged := filepath.Join(root, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	if err := Stage(link, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat the link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file; the operator's release layout is gone")
	}
	if b, _ := os.ReadFile(real); string(b) != "v2" {
		t.Errorf("the target holds %q, want v2 — the upgrade did not land where the link points", b)
	}
	// And the rollback point belongs beside the real binary, not the link.
	if b, _ := os.ReadFile(PreviousPath(real)); string(b) != "v1" {
		t.Errorf("previous beside the target is %q, want v1", b)
	}
	assertNoLitter(t, binDir, "polyemesis")
}

// A ROLLBACK MUST STAY UNDOABLE EVEN WHEN IT HALF FAILS.
//
// If the rollback point cannot be written after the swap, the outgoing version
// exists in exactly one place: the backup temp. Deleting it there destroys the
// only way back to what was running a moment ago. It is kept, and named in the
// error so a person can move it by hand.
func TestAHalfFailedInstallKeepsTheOutgoingBinarySomewhere(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	// A directory at .previous: the rename onto it fails, the install does not.
	if err := os.Mkdir(PreviousPath(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(PreviousPath(bin), "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	err := Stage(bin, staged, hashOf(t, staged))
	if err == nil {
		t.Fatal("Stage reported success though the rollback point could not be written")
	}
	if b, _ := os.ReadFile(bin); string(b) != "v2" {
		t.Errorf("binary is %q, want v2: the install itself succeeded and must be reported as done", b)
	}
	// v1 must still exist somewhere, and the error must say where.
	found := ""
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), backupPrefix) {
			if b, _ := os.ReadFile(filepath.Join(dir, e.Name())); string(b) == "v1" {
				found = e.Name()
			}
		}
	}
	if found == "" {
		t.Fatal("the outgoing binary was deleted; there is now no copy of the version that was running")
	}
	if !strings.Contains(err.Error(), found) {
		t.Errorf("the error does not name the file holding the previous version: %v", err)
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
	// Verification now happens on a copy made inside the install directory, so a
	// rejected download has something to leave behind. Repeated failed upgrades
	// would fill the directory with near-copies of the binary.
	assertNoLitter(t, dir, "polyemesis", "staged")
}

// assertNoLitter fails if the install directory holds anything but the files
// named. Temp files this package makes are all dot-prefixed.
func assertNoLitter(t *testing.T, dir string, allowed ...string) {
	t.Helper()
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !ok[e.Name()] {
			t.Errorf("left %q behind in the install directory", e.Name())
		}
	}
}

// THE BYTES THAT WERE HASHED MUST BE THE BYTES THAT ARE INSTALLED.
//
// Verifying a path and then renaming that same path are two lookups, and
// anything able to write to the staging directory -- world-writable /tmp, where
// downloads land -- can swap the file between them. Stage closes the window by
// copying into the install directory and hashing on the way past, so what it
// renames into place is a file only this process has a name for.
//
// Observable here as: the staged file is not what gets installed. It is still
// sitting there afterwards, untouched, and the live binary is a different file.
func TestStageInstallsItsOwnCopyRatherThanTheStagedPath(t *testing.T) {
	dir := t.TempDir()
	stagingDir := t.TempDir() // as in real life: a different directory, often a different mount
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("old"), 0o755)
	staged := filepath.Join(stagingDir, "download")
	os.WriteFile(staged, []byte("new"), 0o644)

	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if b, err := os.ReadFile(staged); err != nil || string(b) != "new" {
		t.Errorf("the staged download was consumed (%q, %v); Stage must not depend on that path after hashing it", b, err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "new" {
		t.Errorf("binary is %q, want new", b)
	}
	// A rename out of a temp directory crosses a filesystem on a normal install
	// and fails with EXDEV. Copying is what makes a cross-device stage work.
	assertNoLitter(t, dir, "polyemesis", "polyemesis.previous")
}

// A BACKUP THAT MIGHT BE PARTIAL IS WORSE THAN NO BACKUP.
//
// Writing straight to .previous with O_TRUNC destroys the old backup before the
// new one exists: a kill or a full disk mid-copy leaves a truncated file that
// PlanFor still advertises and Rollback still installs, handing systemd a
// fragment of an executable.
//
// Checked by identity rather than by killing the process: a file written in
// place is still the same file afterwards, one renamed into position is not.
// os.SameFile asks exactly that, and portably.
func TestTheBackupIsReplacedAtomicallyNotTruncatedInPlace(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "polyemesis")
	os.WriteFile(bin, []byte("v1"), 0o755)
	prev := PreviousPath(bin)
	os.WriteFile(prev, []byte("a complete earlier backup"), 0o700)
	before := statOf(t, prev)

	staged := filepath.Join(dir, "staged")
	os.WriteFile(staged, []byte("v2"), 0o644)
	if err := Stage(bin, staged, hashOf(t, staged)); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	if os.SameFile(before, statOf(t, prev)) {
		t.Error("the backup was written in place; a kill mid-copy would leave a truncated file " +
			"that PlanFor advertises and Rollback installs")
	}
	if b, _ := os.ReadFile(prev); string(b) != "v1" {
		t.Errorf("previous is %q, want v1", b)
	}
	assertNoLitter(t, dir, "polyemesis", "polyemesis.previous", "staged")
}

func statOf(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	return fi
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
