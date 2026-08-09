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
	// systemd sets INVOCATION_ID for every unit it starts, and nothing else
	// does. Far more reliable than looking for the binary in /usr/local/bin,
	// which says where a file is and nothing about what runs it.
	if env("INVOCATION_ID") != "" {
		return MethodSystemd
	}
	return MethodManual
}

// PreviousPath is where the outgoing binary is kept so a rollback can find it.
//
// Beside the binary rather than in a temp directory: /tmp is cleared on reboot
// on most distributions, and the moment an operator most wants to roll back is
// after a restart that went badly.
func PreviousPath(binary string) string { return binary + ".previous" }

// PlanFor builds the plan for this box.
//
// version is the tag being offered, used only to render a command an operator
// can paste. It is never used to decide anything.
func PlanFor(m Method, binary, version string) Plan {
	p := Plan{Method: m}
	if _, err := os.Stat(PreviousPath(binary)); err == nil {
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
	if err := Verify(staged, wantHex); err != nil {
		return err
	}
	// Executable before it is in place, so the moment after the rename is a
	// moment in which the service could actually start.
	if err := os.Chmod(staged, 0o755); err != nil {
		return err
	}
	prev := PreviousPath(binary)
	// Copied, not renamed: a rename would leave the live binary path empty for
	// an instant, and something restarting in that instant finds nothing.
	if err := copyFile(binary, prev); err != nil {
		return fmt.Errorf("could not keep the outgoing binary for rollback: %w", err)
	}
	if err := os.Rename(staged, binary); err != nil {
		return err
	}
	return nil
}

// Rollback puts the previous binary back. The caller restarts.
func Rollback(binary string) error {
	prev := PreviousPath(binary)
	if _, err := os.Stat(prev); err != nil {
		return errors.New("there is no previous binary to roll back to")
	}
	// The CURRENT one is kept as the previous, so a rollback can be undone by a
	// second rollback. An operator who rolls back by mistake at 3am should not
	// need the release page to get out of it.
	tmp := binary + ".rollback-tmp"
	if err := copyFile(binary, tmp); err != nil {
		return err
	}
	if err := os.Rename(prev, binary); err != nil {
		os.Remove(tmp)
		return err
	}
	// Back to the live mode. The backup was kept owner-only; the thing now at
	// the binary path has to be executable by whoever runs the service, which
	// is not necessarily the account that took the backup.
	if err := os.Chmod(binary, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, prev)
}

// copyFile duplicates src to dst, owner-only.
//
// 0o700, NOT 0o755. Only the LIVE binary needs to be executable by anyone other
// than its owner, and it gets that mode explicitly at the moment it becomes
// live. A backup sitting beside it is read by exactly one thing -- a rollback,
// running as the same user -- so world read and execute on it buys nothing and
// widens what a local account can reach. Sonar's S2612 flagged this and it was
// right to.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
