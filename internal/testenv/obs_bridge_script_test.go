package testenv

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// integrations/obs/polyemesis_obs.py sent its bearer token to any address
// typed into it, http:// to a remote host included (poka-yoke audit
// 2026-11-04). Its own unittest pins the rule; this runs it under go test so
// CI does not need a separate Python step. Skipped where there is no python3.
func TestOBSBridgeScriptRefusesCleartextAddresses(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	script := filepath.Join(repoRootFromTest(t), "integrations", "obs", "test_polyemesis_obs.py")
	cmd := exec.Command(py, "-I", script)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the OBS bridge script's tests failed: %v\n%s", err, out)
	}
}
