package main

import (
	"os"
	"strings"
	"testing"
)

// TestTheQuotaMeterIsInstalledBeforeAnyLoopCanCallYouTube (F2): the
// preannounce, recurring, lifecycle and rollover loops each sweep at
// startup, and a YouTube write made before the meter is installed bypasses
// the shared allowance. The meter goes in right after the server is built,
// before the first loop starts.
func TestTheQuotaMeterIsInstalledBeforeAnyLoopCanCallYouTube(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	built := strings.Index(src, "srv := api.New(")
	meter := strings.Index(src, "api.InstallYouTubeMeter(srv)")
	if built < 0 || meter < 0 {
		t.Fatal("main.go no longer builds the server or installs the meter where this test looks")
	}
	for _, loop := range []string{
		"go srv.RefreshLoop(", "go srv.PreannounceLoop(", "go srv.RecurringLoop(",
		"go srv.LifecycleLoop(", "go srv.RolloverLoop(", "go srv.AnalyticsLoop(",
		"srv.StartChat(", "go obsBridge.Run(",
	} {
		at := strings.Index(src, loop)
		if at < 0 {
			t.Fatalf("main.go no longer has %q", loop)
		}
		if at < meter {
			t.Errorf("%s starts before the YouTube quota meter is installed", loop)
		}
	}
	if meter < built {
		t.Fatal("the meter is installed before the server exists")
	}
}
