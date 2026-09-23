package testenv

import (
	"regexp"
	"strings"
	"testing"
)

// AN INTERRUPTED INSTALL MUST ROLL BACK, WHATEVER $? SAYS.
//
// install.sh's cleanup_on_failure takes "exit status 0" to mean the install
// succeeded. Armed for INT and TERM with no argument, it read $? -- the status
// of the last command that finished -- and a signal sent to the script itself
// (timeout, kill, systemd) while a command was running is handled only after
// that command returns, usually 0. The handler then exited 0 with nothing
// undone: a half-made install reported as a clean one. The installer
// workflow's interrupt step caught it on main at 1c0c77dd ("rollback announced
// 0 times"). Reproduced with SIGTERM: exit 0 and no rollback before, exit 143
// and a rollback after.
//
// Read from the source because the behaviour needs a Linux host with systemd
// and Docker to run for real; that run is the installer workflow's.
func TestInstallerSignalTrapsPassTheirOwnStatus(t *testing.T) {
	src := installerSource(t)

	for sig, code := range map[string]string{"INT": "130", "TERM": "143"} {
		re := regexp.MustCompile(`(?m)^trap 'cleanup_on_failure ` + code + `' ` + sig + `$`)
		if !re.MatchString(src) {
			t.Errorf("install.sh does not trap %s as `trap 'cleanup_on_failure %s' %s`: an %s "+
				"that lands while a command is running reaches the handler with $? = 0, and the "+
				"install exits 0 with nothing rolled back", sig, code, sig, sig)
		}
	}
	if regexp.MustCompile(`(?m)^trap cleanup_on_failure [^\n]*\b(INT|TERM)\b`).MatchString(src) {
		t.Error("install.sh still arms cleanup_on_failure for INT or TERM without a status")
	}

	body := src[strings.Index(src, "cleanup_on_failure() {"):]
	body = body[:strings.Index(body, "\n}\n")]
	if !strings.Contains(body, `[ -n "${1:-}" ] && code=$1`) {
		t.Error("cleanup_on_failure ignores the status its signal traps pass it")
	}
}
