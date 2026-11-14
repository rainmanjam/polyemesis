package drivercheck

import (
	"os"
	"os/exec"
	"testing"
)

// TestDemoSeedDriverStillCompiles: scripts/demo_seed_driver.go is //go:build
// ignore, so `go build ./...` never sees it. It imports internal/demorig and
// internal/recording/segname; a change to either would otherwise break the demo
// rig without any local signal. Same rationale as TestEveryAcceptanceDriverStillCompiles.
func TestDemoSeedDriverStillCompiles(t *testing.T) {
	out, err := exec.Command("go", "build", "-o", os.DevNull, "../../scripts/demo_seed_driver.go").CombinedOutput()
	if err != nil {
		t.Fatalf("the demo seed driver no longer compiles:\n%s", out)
	}
}
