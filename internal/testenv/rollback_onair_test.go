package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// rollbackScript is the generated rollback.sh's heredoc, as install.sh writes it.
func rollbackScript(t *testing.T, src string) string {
	t.Helper()
	const open = "cat > \"$INSTALL_DIR/rollback.sh\" <<EOF"
	start := strings.Index(src, open)
	if start < 0 {
		t.Fatal("install.sh no longer writes rollback.sh; this guard would check nothing")
	}
	end := strings.Index(src[start:], "\nEOF\n")
	if end < 0 {
		t.Fatal("the rollback.sh heredoc is never closed")
	}
	return src[start : start+end]
}

// rollback.sh stops the service and overwrites the database. It used to do
// both with no on-air check and no word about what is lost (poka-yoke audit
// 2026-11-04). It must carry the same two-step guard update.sh does.
func TestTheGeneratedRollbackRefusesWhileOnAirAndSaysWhatIsLost(t *testing.T) {
	r := rollbackScript(t, mustReadRepoFile(t, repoRootFromTest(t), "scripts/install.sh"))
	for _, want := range []struct{ text, why string }{
		{"publishing_now()", "no on-air probe: a rollback ends a live broadcast"},
		{"REFUSING: this install is publishing right now", "a probe that never refuses gates nothing"},
		{"--force", "no supported way past the guard"},
		{"cgroup.procs", "the probe is not scoped to this unit's cgroup"},
		{"will be lost", "it does not say what the rollback throws away"},
		{"[ -t 0 ]", "it does not ask on a terminal before overwriting the database"},
	} {
		if !strings.Contains(r, want.text) {
			t.Errorf("generated rollback.sh lacks %q: %s", want.text, want.why)
		}
	}
	// Order matters: the guard and the question come before the stop.
	guard := strings.Index(r, "REFUSING: this install is publishing right now")
	stop := strings.Index(r, "systemctl stop")
	if guard < 0 || stop < 0 || guard > stop {
		t.Error("the on-air refusal must come before the service is stopped")
	}
}

// The real generator, run: the script it writes parses its arguments in either
// order, and restores a backup when told to go ahead.
func TestTheGeneratedRollbackRuns(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	root := repoRootFromTest(t)
	tmp := t.TempDir()
	install := filepath.Join(tmp, "opt")
	data := filepath.Join(tmp, "data")
	bin := filepath.Join(tmp, "polyemesis")
	backup := data + ".bak-2026-11-04-0101"
	for _, d := range []string{install, data, backup} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "polyemesis.db"), "newer", 0o644)
	write(filepath.Join(backup, "polyemesis.db"), "older", 0o644)
	write(filepath.Join(backup, "secret.key"), "k", 0o600)
	write(filepath.Join(backup, "polyemesis.previous"), "#!/bin/sh\n", 0o755)

	script := generateRollback(t, root, install, data, bin)

	cmd := exec.Command("bash", script, "--force", backup)
	cmd.Stdin = nil // not a terminal: no prompt
	cmd.Env = stubSystemctlEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rollback.sh --force <backup>: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "will be lost") {
		t.Errorf("rollback.sh did not say what it throws away:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(data, "polyemesis.db"))
	if string(got) != "older" {
		t.Errorf("database after rollback = %q, want the backup's", got)
	}
}

// generateRollback runs install.sh's own generator, so tests run the script
// install.sh actually writes rather than a copy of it.
func generateRollback(t *testing.T, root, install, data, bin string) string {
	t.Helper()
	// The heredoc has column-0 braces of its own, so the function is taken up
	// to its chmod line rather than to the first "}".
	gen := `set -e
eval "$(sed -n '/^write_binary_rollback_script() {/,/chmod +x "\$INSTALL_DIR\/rollback.sh"/p' "$1/scripts/install.sh"; echo '}')"
INSTALL_DIR="$2" DATA_DIR="$3" BIN_PATH="$4" SERVICE_NAME=polyemesis-test write_binary_rollback_script`
	if out, err := exec.Command("bash", "-c", gen, "gen", root, install, data, bin).CombinedOutput(); err != nil {
		t.Fatalf("generating rollback.sh: %v\n%s", err, out)
	}
	script := filepath.Join(install, "rollback.sh")
	if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("generated rollback.sh does not parse: %v\n%s", err, out)
	}
	return script
}

// stubSystemctlEnv puts a systemctl first on PATH that touches nothing: the
// service is never "active", and start succeeds without doing anything. So
// these tests run on systemd hosts (CI) too, instead of skipping there.
func stubSystemctlEnv(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/bin/sh\ncase \"$1\" in is-active) exit 3 ;; *) exit 0 ;; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The refusal, run: an ffmpeg publishing in this unit's cgroup stops the
// rollback without --force, leaves the database alone, and names the PID --
// never the command line, which carries the stream key.
func TestTheGeneratedRollbackRefusesWhileAProcessPublishes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/<pid>/cmdline")
	}
	root := repoRootFromTest(t)
	tmp := t.TempDir()
	install, data := filepath.Join(tmp, "opt"), filepath.Join(tmp, "data")
	backup := data + ".bak-2026-11-04-0101"
	for _, d := range []string{install, data, backup} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{
		filepath.Join(data, "polyemesis.db"):         "newer",
		filepath.Join(backup, "polyemesis.db"):       "older",
		filepath.Join(backup, "secret.key"):          "k",
		filepath.Join(backup, "polyemesis.previous"): "#!/bin/sh\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := generateRollback(t, root, install, data, filepath.Join(tmp, "polyemesis"))

	// A process whose argv looks like a publishing ffmpeg, in a fake cgroup.
	const key = "streamkey-sentinel"
	pub := exec.Command("bash", "-c", `exec -a "ffmpeg -i udp://127.0.0.1:1 -f flv rtmp://live.example/app/`+key+`" sleep 30`)
	if err := pub.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Process.Kill(); _ = pub.Wait() })
	cg := filepath.Join(tmp, "cgroup")
	unit := filepath.Join(cg, "system.slice", "polyemesis-test.service")
	if err := os.MkdirAll(unit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unit, "cgroup.procs"), []byte(strconv.Itoa(pub.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", script, backup)
	cmd.Env = append(stubSystemctlEnv(t), "POLYEMESIS_CGROUP_ROOT="+cg)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("rollback.sh ran while a broadcast was on air:\n%s", out)
	}
	if !strings.Contains(string(out), "REFUSING") {
		t.Errorf("it failed without the refusal:\n%s", out)
	}
	if strings.Contains(string(out), key) {
		t.Errorf("the refusal printed the stream key:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "polyemesis.db")); string(got) != "newer" {
		t.Errorf("the refused rollback overwrote the database with %q", got)
	}
}
