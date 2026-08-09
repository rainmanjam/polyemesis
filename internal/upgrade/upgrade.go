// Package upgrade decides what "upgrade" means on the box it is running on,
// and refuses to guess.
//
// There is no single upgrade action, and writing one is the mistake this
// package exists to avoid. Replacing the binary inside a container works until
// the container is recreated, at which point the operator is silently back on
// the old image and has no idea why. Fetching a tarball over a package-managed
// install fights the package manager. The only honest move is to detect how
// polyemesis was installed and either do the right thing or say the right thing.
//
// NOTHING HERE RESTARTS ANYTHING BY ITSELF. polyemesis carries live broadcasts;
// a restart drops every destination mid-stream. The caller supplies the off-air
// decision (see engine.OnAir) and this package supplies the mechanics.
package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Method is how this install was put on the box.
type Method string

const (
	// MethodDocker is a container. The IMAGE is the unit of upgrade, so the
	// only correct action is to tell the operator which command to run against
	// their orchestrator -- this process cannot change what the next `docker
	// run` starts.
	MethodDocker Method = "docker"
	// MethodSystemd is a service unit. The one case where this package can
	// safely act: the binary can be staged beside the running one and systemd
	// asked to restart onto it.
	MethodSystemd Method = "systemd"
	// MethodManual is a binary someone put somewhere and runs themselves.
	// Reported rather than acted on: we do not know what supervises it, and
	// replacing a binary out from under an unknown supervisor is how a service
	// comes back as something nobody chose.
	MethodManual Method = "manual"
)

// Plan is what can be done about an available release, on this box.
type Plan struct {
	Method Method `json:"method"`
	// Automatic reports whether this process can perform the upgrade itself.
	// True only for systemd.
	Automatic bool `json:"automatic"`
	// Command is what the operator should run when Automatic is false. Empty
	// otherwise. Shown verbatim, so it must be correct rather than indicative.
	Command string `json:"command,omitempty"`
	// BinaryPath is what would be replaced. Empty when Automatic is false.
	BinaryPath string `json:"binaryPath,omitempty"`
	// RollbackAvailable reports that a previous binary is staged and could be
	// restored. See PreviousPath.
	RollbackAvailable bool `json:"rollbackAvailable"`
	// Reason explains a refusal that is not about being on air -- an unwritable
	// binary directory, an unidentifiable install. Empty when the plan is
	// actionable.
	Reason string `json:"reason,omitempty"`
}

// Detect works out how this install was made.
//
// Order matters. The container check is first because a container can also have
// systemd inside it, and being in a container is the stronger fact: whatever
// supervises the process, the image still decides what comes back.
func Detect(env func(string) string, exists func(string) bool) Method {
	if env == nil {
		env = os.Getenv
	}
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}

	// Written by Docker into every container it starts, and the check every
	// other tool uses.
	if exists("/.dockerenv") {
		return MethodDocker
	}
	// Podman and some Kubernetes runtimes do not write /.dockerenv.
	if exists("/run/.containerenv") {
		return MethodDocker
	}
	// Kubernetes injects this into every container and does not write either of
	// the files above; containerd and CRI-O on their own write neither. Same
	// conclusion as Docker: the image decides what comes back.
	if env("KUBERNETES_SERVICE_HOST") != "" {
		return MethodDocker
	}
	if supervisedByAUnit(env, exists) {
		return MethodSystemd
	}
	return MethodManual
}

// procSelfCgroup is a variable so a test can point it at a fixture. There is no
// portable way to ask this question and no reason to: it is a Linux file, and
// systemd is a Linux program.
var procSelfCgroup = "/proc/self/cgroup"

// supervisedByAUnit reports whether THIS process is the process of a systemd
// service -- not merely running on a systemd box, and not merely descended from
// something that was.
//
// The distinction is the whole point. Saying "systemd" sets Plan.Automatic,
// which is a promise that after this package replaces the binary, something
// will restart onto it. If nothing supervises this process, that promise is
// false: the operator is left running a version that no longer exists on disk,
// with no restart coming and no sign anything is wrong.
//
// Three signals, each covering the previous one's gap:
//
//   - INVOCATION_ID is set by systemd for every unit it starts. On its own it
//     proves nothing, because it is an ordinary environment variable and every
//     child inherits it -- a shell spawned from `systemctl status`, a script in
//     a timer, a binary launched by hand from any of those.
//   - /run/systemd/system is what sd_booted(3) checks; it exists only when
//     systemd is the running init. That rules out the variable being set on a
//     box with no systemd at all, and nothing more. On a systemd host -- which
//     is the normal case -- it is always true and settles nothing.
//   - The cgroup is the one that answers the actual question. A unit's own
//     process sits in a cgroup whose last component is its unit, so the path
//     ends in ".service". A login shell sits in a session ".scope", and so does
//     anything it launches, INVOCATION_ID inherited or not.
func supervisedByAUnit(env func(string) string, exists func(string) bool) bool {
	if env("INVOCATION_ID") == "" {
		return false
	}
	if !exists("/run/systemd/system") {
		return false
	}
	b, err := os.ReadFile(procSelfCgroup)
	if err != nil {
		// Unreadable: a hardened container, a kernel without cgroups, a system
		// that is not Linux. Fall back to the two weaker signals rather than
		// refusing -- the previous line already established systemd is init, and
		// treating a genuine unit as manual would leave an operator with no
		// upgrade path at all. Wrong in the safe direction is the other branch's
		// job; this one has already cleared the cheap tests.
		return true
	}
	conclusive := false
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		// "0::/system.slice/polyemesis.service" (v2), or
		// "1:name=systemd:/system.slice/polyemesis.service" (v1).
		path := line
		if i := strings.LastIndex(line, ":"); i >= 0 {
			path = line[i+1:]
		}
		// A cgroup NAMESPACE reports membership relative to its own root, so a
		// unit that has one -- which systemd gives to anything with
		// PrivateMounts, and every container runtime sets -- reads its own
		// cgroup as "0::/". That is not evidence of being unsupervised; it is
		// the absence of evidence, and treating it as a login session would
		// deny a genuine unit its upgrade path.
		if path == "" || path == "/" {
			continue
		}
		conclusive = true
		// The LAST component, not the whole path. A user session lives under
		// "user@1000.service", so any process a person starts by hand has
		// ".service" somewhere in its cgroup -- but its own leaf is a ".scope".
		if strings.HasSuffix(filepath.Base(path), ".service") {
			return true
		}
	}
	// Nothing the cgroup said bore on the question. Fall back to the weaker
	// signals, as with an unreadable file.
	return !conclusive
}

// PreviousPath is where the outgoing binary is kept so a rollback can find it.
//
// Beside the binary rather than in a temp directory: /tmp is cleared on reboot
// on most distributions, and the moment an operator most wants to roll back is
// after a restart that went badly.
func PreviousPath(binary string) string { return binary + ".previous" }

// RescuedPath is where a binary ends up when it could not be filed as the
// rollback point but must not be thrown away. See install.
//
// A separate name from PreviousPath on purpose. It is NOT a rollback point: it
// exists because something went wrong, it is not advertised by PlanFor, and
// restoring it is a decision a person makes rather than a button. Also outside
// the temp prefixes, so the stale sweep leaves it alone.
//
// ONE FIXED NAME, so a second rescue replaces the first. That is deliberate and
// it was argued: a unique name per rescue would keep every older version, but
// nothing would ever remove them, and the sweep cannot -- this name is excluded
// from it precisely so a rescued binary survives. An install directory that
// silently accumulates binary-sized files forever is a worse fault than the one
// it would fix.
//
// Nothing valuable is lost. What is rescued is always the version that was
// running immediately before the current one, which is the same thing
// PreviousPath holds and the only version a recovery would sensibly target. A
// second half-failure means the box moved on again, and the copy worth keeping
// moved with it.
func RescuedPath(binary string) string { return binary + ".previous-rescued" }

// PlanFor builds the plan for this box.
//
// version is the tag being offered, used only to render a command an operator
// can paste. It is never used to decide anything.
func PlanFor(m Method, binary, version string) Plan {
	p := Plan{Method: m}
	// Beside the RESOLVED binary, because that is where Stage put it.
	if _, err := os.Stat(PreviousPath(resolve(binary))); err == nil {
		p.RollbackAvailable = true
	}

	switch m {
	case MethodDocker:
		// The image tag, not `docker pull` alone: pulling changes nothing until
		// something recreates the container, and an operator who runs only the
		// pull will reasonably believe they have upgraded.
		p.Command = fmt.Sprintf("docker compose pull && docker compose up -d   # or: docker pull rainmanjam/polyemesis:%s", version)
		return p

	case MethodManual:
		p.Command = fmt.Sprintf("re-run scripts/install.sh, or download polyemesis-%s-%s-%s from the release page",
			version, runtime.GOOS, runtime.GOARCH)
		return p

	case MethodSystemd:
		if binary == "" {
			p.Reason = "the running binary's path could not be determined"
			return p
		}
		// The path the upgrade will actually write to, which is not the given
		// one when it is a symlink. Reported as BinaryPath too: an operator
		// being told what is about to be replaced should be told the truth.
		binary = resolve(binary)
		// Writability of the DIRECTORY, not the file: an upgrade replaces the
		// file by renaming a staged one over it, which needs the directory.
		if err := writable(filepath.Dir(binary)); err != nil {
			p.Reason = fmt.Sprintf("%s is not writable by this process (%v); upgrade with sudo, or re-run scripts/install.sh", filepath.Dir(binary), err)
			return p
		}
		p.Automatic = true
		p.BinaryPath = binary
		return p
	}
	p.Reason = "unrecognised install method"
	return p
}

// writable reports whether this process can create a file in dir.
//
// Tested by actually creating one. Checking the mode bits answers a different
// question -- one that is wrong under a read-only mount, a full disk, or any
// mandatory access control, all of which are ordinary on the kind of box this
// runs on.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".polyemesis-upgrade-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// ErrChecksumMismatch is returned when a downloaded artefact does not match the
// published checksum. Deliberately its own error: a caller has to be able to
// tell "the download is wrong" from "the download failed", because one of those
// is a network blip and the other is not.
var ErrChecksumMismatch = errors.New("checksum does not match the published SHA256SUMS")

// Verify checks a staged file against an expected hex sha256.
//
// MANDATORY, not optional, and it is why this package takes a checksum rather
// than fetching one itself: an unverified self-updater on a box with a public
// ingest port is a real attack surface. The release workflow publishes
// SHA256SUMS beside every binary, so the honest input is available.
func Verify(path, wantHex string) error {
	if strings.TrimSpace(wantHex) == "" {
		return errors.New("no expected checksum was supplied; refusing to install an unverified binary")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, strings.TrimSpace(wantHex)) {
		return fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, got, wantHex)
	}
	return nil
}

// ChecksumFor pulls one file's hash out of a SHA256SUMS body.
//
// Matches on the BASENAME, because the published file lists paths as they were
// on the build machine ("dist/polyemesis-v0.5.0-linux-amd64") and the caller
// knows the artefact by its name.
func ChecksumFor(sums, artefact string) (string, error) {
	want := filepath.Base(artefact)
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if filepath.Base(strings.TrimPrefix(fields[1], "*")) == want {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %q in SHA256SUMS", want)
}

// Stage puts a verified binary in place, keeping the outgoing one for rollback.
//
// ORDER IS THE WHOLE THING. The new binary is written and verified BEFORE
// anything is moved, so a failed download or a bad checksum leaves a running
// install completely untouched. Only once there is a good file on disk is the
// outgoing binary set aside and the new one renamed over it -- and a rename
// within one directory is atomic, so there is no instant at which the path
// exists but holds a partial file.
//
// Does not restart anything. The caller decides when, having asked whether
// anything is on air.
func Stage(binary, staged, wantHex string) error {
	if strings.TrimSpace(wantHex) == "" {
		return errors.New("no expected checksum was supplied; refusing to install an unverified binary")
	}
	// Upgrade what the path POINTS AT. See resolve.
	binary = resolve(binary)
	// PRESERVE THE MODE THE INSTALL ALREADY HAD, rather than asserting one.
	//
	// Hardcoding 0o755 was both a Sonar S2612 finding and, more importantly,
	// wrong: an operator who deliberately installed the binary 0o750 does not
	// expect an upgrade to widen it to world-executable. The live file is the
	// authority on what mode this install uses, so the replacement inherits it.
	mode, err := liveMode(binary)
	if err != nil {
		return err
	}

	// THE FILE THAT IS HASHED MUST BE THE FILE THAT IS INSTALLED.
	//
	// Verifying `staged` by path and then renaming `staged` by path are two
	// separate lookups, and anything that can write to the staging directory can
	// swap the file between them. Downloads land in a temp directory, which on a
	// normal box is world-writable, so this is not a theoretical window: the
	// checksum would pass on one file and a different one would be installed.
	//
	// So the bytes are copied into the INSTALL directory and hashed on the way
	// past, and it is that copy -- reachable only by a name this process just
	// created -- that gets renamed into place. There is no second lookup of an
	// attacker-reachable path.
	//
	// It also fixes a plainer bug: renaming from a temp directory to /usr/local/bin
	// crosses a filesystem on most installs, and os.Rename fails with EXDEV.
	dir := filepath.Dir(binary)
	sweepStaleTemps(dir)

	incoming, sum, err := copyIntoDir(staged, dir, incomingPrefix, 0o600)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		// Only if it is still ours. Once the rename below consumes this name the
		// path is free, and on a directory another principal can write to,
		// removing it blind would delete whatever they put there next.
		if !installed {
			os.Remove(incoming)
		}
	}()

	if !strings.EqualFold(sum, strings.TrimSpace(wantHex)) {
		return fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, sum, wantHex)
	}
	// Made executable BEFORE it is in place, and not before it is verified. The
	// instant after the rename has to be one in which the service could start,
	// and no instant before the checksum passes may be one in which an
	// unverified download is executable by anybody.
	if err := setModeDurably(incoming, mode); err != nil {
		return err
	}
	installed, err = install(binary, incoming, dir)
	return err
}

// install makes incoming the live binary and the outgoing one the rollback
// point, and reports whether the live binary was actually replaced.
//
// THE ORDER IS CHOSEN SO THAT EVERY INTERMEDIATE STATE IS A RUNNABLE BOX. This
// process can be killed between any two lines here, and a power cut can discard
// anything not yet synced, so "what is on disk if we stop right now" has to have
// a good answer at every point rather than at the end.
//
// The outgoing binary is copied aside FIRST but promoted to .previous LAST. That
// ordering is the fix for two separate faults:
//
//   - Writing .previous before the install means a failed install (EIO, a full
//     disk) has already overwritten the rollback point with a copy of the
//     version that is still running. The operator asked to upgrade, the upgrade
//     failed, and the way back to the version before THAT is now gone.
//   - Renaming the outgoing binary aside, rather than copying it, leaves the
//     binary path empty for an instant. Anything restarting in that instant
//     finds nothing at all.
//
// A kill between the two renames leaves the new binary live and complete, with
// .previous still pointing at the version before the last one. The rollback
// target is one release stale; nothing on disk is broken. That is the worst
// state this function can be interrupted into, and it is an acceptable one.
func install(binary, incoming, dir string) (bool, error) {
	// 0o700: only the LIVE binary needs to be executable by whoever runs the
	// service. A backup beside it is read by exactly one thing -- a rollback,
	// running as the same user -- so group and world access on it buys nothing
	// and widens what a local account can reach. Sonar's S2612 flagged this.
	backup, _, err := copyIntoDir(binary, dir, backupPrefix, 0o700)
	if err != nil {
		return false, fmt.Errorf("could not keep the outgoing binary for rollback: %w", err)
	}
	// Both files are now complete and synced, but the DIRECTORY entries that
	// name them are not. Without this, a power cut after the renames below can
	// come back to a directory that never learned either file existed.
	if err := syncDir(dir); err != nil {
		os.Remove(backup)
		return false, err
	}
	if err := os.Rename(incoming, binary); err != nil {
		os.Remove(backup)
		return false, err
	}
	// Past this point the upgrade has happened and must be reported as such,
	// even if keeping the rollback point fails: the caller has to know the live
	// binary changed.
	if err := os.Rename(backup, PreviousPath(binary)); err != nil {
		// THE BACKUP IS NOT DELETED HERE, and that is deliberate. It is now the
		// only copy of the version that was running a moment ago -- deleting it
		// because the rename failed would destroy the very thing it exists to
		// preserve, which for a rollback means the operator can no longer undo
		// what they just did.
		//
		// But it cannot stay under a temp name either. sweepStaleTemps removes
		// anything carrying these prefixes once it is an hour old, so the next
		// upgrade -- including one that then fails its own checksum and changes
		// nothing -- would quietly delete the copy this branch went out of its
		// way to keep. It gets a name the sweep does not touch and a human can
		// recognise.
		kept := backup
		if err := os.Rename(backup, RescuedPath(binary)); err == nil {
			kept = RescuedPath(binary)
		}
		return true, fmt.Errorf("the new binary is installed but the rollback point could not be "+
			"written: the previous version is at %s, move it to %s to restore it: %w",
			kept, PreviousPath(binary), err)
	}
	return true, syncDir(dir)
}

const (
	incomingPrefix = ".polyemesis-incoming-"
	backupPrefix   = ".polyemesis-outgoing-"
)

// sweepStaleTemps clears temp files a killed run left behind.
//
// Nothing in-process can clean up after SIGKILL, so the next run does it.
// Without this an install directory accumulates a near-copy of the binary for
// every upgrade that was interrupted.
//
// STALE, not merely present. An upgrade in flight owns temp files of exactly
// these names, and a second upgrade starting a moment later would otherwise
// unlink them out from under the first. An age limit avoids needing a lock for
// something that has no legitimate concurrent case: a copy of the binary takes
// seconds, so anything untouched for an hour was abandoned.
//
// Regular files only. A directory sharing the prefix is not something this
// package made, and removing things it did not create is not its business.
func sweepStaleTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, incomingPrefix) && !strings.HasPrefix(n, backupPrefix) {
			continue
		}
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < staleAfter {
			continue
		}
		os.Remove(filepath.Join(dir, n))
	}
}

// staleAfter is how long a temp file must be untouched before a later run
// treats it as debris from a killed one.
const staleAfter = time.Hour

// copyIntoDir writes src into dir under a fresh private name, at mode, durably,
// and returns that name with the sha256 of what was written.
//
// ONE PASS. The hash is taken of the bytes as they are written, so the file that
// was hashed is necessarily the file that exists afterwards. Hashing a second
// read of the same path would reintroduce exactly the gap this exists to close.
//
// The mode is applied through the open descriptor and before the sync, so the
// permissions are as durable as the contents. A file that comes back from a
// power cut with the right bytes and the wrong mode is still a binary the
// service cannot start.
func copyIntoDir(src, dir, prefix string, mode os.FileMode) (string, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()

	out, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", "", err
	}
	name := out.Name()
	fail := func(err error) (string, string, error) {
		out.Close()
		os.Remove(name)
		return "", "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		return fail(err)
	}
	if err := out.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(name)
		return "", "", err
	}
	return name, hex.EncodeToString(h.Sum(nil)), nil
}

// setModeDurably changes a file's mode and makes that change survive a crash.
//
// Split out from copyIntoDir because an incoming binary must not be executable
// until its checksum has passed, so its final mode is set later than its bytes.
func setModeDurably(path string, mode os.FileMode) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Sync()
}

// syncDir makes the directory's own entries durable.
//
// fsync on a file promises the CONTENTS survive a power cut. It promises
// nothing about the name: a rename is a change to the directory, and the
// directory has to be synced for it to be guaranteed too. Skipping this is the
// classic way an atomic-rename install turns out not to be.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		// No directory handle to fsync on Windows, and nothing to emulate it
		// with. NTFS journals the metadata operation instead.
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Rollback puts the previous binary back. The caller restarts.
//
// The same swap as Stage, with .previous as the source and no checksum -- these
// bytes were verified when they were installed. Sharing install() is deliberate:
// a rollback is the operation that runs when something has already gone wrong,
// and it must not have crash-safety weaker than the upgrade that led to it.
//
// The CURRENT binary becomes the new .previous, so a rollback taken by mistake
// can be undone by a second rollback. Nobody should need the release page at 3am.
func Rollback(binary string) error {
	binary = resolve(binary)
	// Read before anything moves: afterwards the live path holds the file that
	// was being kept owner-only, and its mode is not the one to restore.
	mode, err := liveMode(binary)
	if err != nil {
		return err
	}
	prev := PreviousPath(binary)
	if _, err := os.Stat(prev); err != nil {
		return errors.New("there is no previous binary to roll back to")
	}
	dir := filepath.Dir(binary)
	sweepStaleTemps(dir)

	// COPIED out of .previous rather than renamed. Renaming it away means a kill
	// before the swap completes leaves the rollback point gone and the only copy
	// of the outgoing version in a temp file nothing knows how to find -- which
	// is how a rollback ends with a box that can neither go back nor forward.
	incoming, _, err := copyIntoDir(prev, dir, incomingPrefix, 0o600)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			os.Remove(incoming)
		}
	}()
	// Executable before it is live, not after. Doing it afterwards leaves a
	// window in which the binary path holds a file the service cannot execute,
	// and the version being rolled back TO is the one that must work.
	if err := setModeDurably(incoming, mode); err != nil {
		return err
	}
	installed, err = install(binary, incoming, dir)
	return err
}

// resolve follows symlinks to the file an upgrade should actually replace.
//
// os.Executable can hand back a path that is a symlink -- /usr/local/bin/poly-
// emesis pointing into /opt/polyemesis/v0.5.0/ is how release managers and
// hand-rolled install scripts keep versions side by side. Renaming over the LINK
// replaces the indirection with a plain file, silently dismantling the scheme
// the operator built and stranding whatever the link pointed at.
//
// Every path this package touches is derived from the resolved one, so the
// backup lands beside the real binary and the writability check asks about the
// directory that will actually be written to.
//
// An unresolvable path is returned unchanged: the error belongs to whichever
// operation tries to use it, which can say what it was doing at the time.
func resolve(binary string) string {
	if binary == "" {
		return binary
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return binary
	}
	return real
}

// liveMode is the permission bits the installed binary currently carries, with
// a floor of owner-executable.
//
// The floor exists because the alternative is an upgrade that silently produces
// a binary the service cannot start. If the live file is somehow not executable
// the install is already broken, and refusing to make the replacement runnable
// would turn that into an outage at exactly the wrong moment.
func liveMode(binary string) (os.FileMode, error) {
	fi, err := os.Stat(binary)
	if err != nil {
		return 0, err
	}
	m := fi.Mode().Perm()
	return m | 0o100, nil
}
