package testenv

import (
	"strings"
	"testing"
)

// BOTH GENERATED UPDATERS MUST REFUSE WHILE A BROADCAST IS ON AIR.
//
// scripts/install.sh generates an update.sh per install mode. The compose one
// has refused since it was written; the binary one did not, so the SAME
// operator action was safe in one mode and silently cut a live stream in the
// other. Both stop the service to take a consistent copy of a WAL database, and
// a broadcast that ends cannot be resumed.
//
// Detection rung, and it can only be that from here: the generated scripts do
// not exist until somebody runs the installer, so this reads the generator.
// scripts/acceptance-install.sh exercises the compose guard for real, against a
// stub that answers `top` two ways.
//
// Scoped to the update.sh HEREDOCS rather than counting matches across the
// file: uninstall.sh carries its own copy of this guard, so a whole-file count
// answers a different question and drifts every time either script changes.
func updateScripts(t *testing.T, src string) []string {
	t.Helper()
	const open = "cat > \"$INSTALL_DIR/update.sh\" <<EOF"
	var out []string
	for i := 0; ; {
		j := strings.Index(src[i:], open)
		if j < 0 {
			break
		}
		start := i + j
		end := strings.Index(src[start:], "\nEOF\n")
		if end < 0 {
			t.Fatalf("an update.sh heredoc at offset %d is never closed", start)
		}
		out = append(out, src[start:start+end])
		i = start + end
	}
	if len(out) != 2 {
		t.Fatalf("found %d update.sh generators, want 2 (binary and compose).\n\n"+
			"A new install mode needs the same on-air guard as the other two, and this "+
			"is where its absence shows.", len(out))
	}
	return out
}

func TestBothGeneratedUpdatersRefuseWhileOnAir(t *testing.T) {
	src := mustReadRepoFile(t, repoRootFromTest(t), "scripts/install.sh")
	for i, u := range updateScripts(t, src) {
		if !strings.Contains(u, "publishing_now()") {
			t.Errorf("generated update.sh #%d defines no publishing_now().\n\n"+
				"It stops the service to copy a WAL database. Without this the upgrade "+
				"ends a live broadcast with no warning, and a broadcast that ends cannot "+
				"be resumed.", i+1)
			continue
		}
		if !strings.Contains(u, "REFUSING: this install is publishing right now") {
			t.Errorf("generated update.sh #%d probes but never refuses.\n\n"+
				"Defining a check and not acting on it is the shape this repository keeps "+
				"finding: it reports, and gates nothing.", i+1)
		}
		if !strings.Contains(u, "--force") {
			t.Errorf("generated update.sh #%d offers no --force.\n\n"+
				"A guard with no supported way past it is one operators route around by "+
				"editing the script, which is worse than one they override deliberately.", i+1)
		}
		// The probe must be scoped to THIS install. Compose asks `compose top`;
		// binary asks the unit's own cgroup. A bare `pgrep ffmpeg` answers "is
		// anything on this host encoding" and refuses upgrades over an unrelated
		// install or the operator's own terminal.
		if !strings.Contains(u, "cgroup.procs") && !strings.Contains(u, "COMPOSE_CMD top") {
			t.Errorf("generated update.sh #%d scopes its probe to neither the unit's "+
				"cgroup nor the compose project; it cannot tell this install's ffmpeg "+
				"from any other on the host", i+1)
		}
	}
}
