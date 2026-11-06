//go:build windows

package main

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/engine"
)

// The SCM kills the service when it is silent for longer than the wait hint.
// The hint must outlast the shutdown budget, or a graceful stop is killed
// mid-flush: the truncated-recording failure the budget exists to prevent.
func TestStopWaitHintOutlastsTheShutdownBudget(t *testing.T) {
	if stopWaitHint <= engine.ShutdownBudget {
		t.Fatalf("stopWaitHint %s does not outlast engine.ShutdownBudget %s", stopWaitHint, engine.ShutdownBudget)
	}
	if stopWaitHint != engine.ShutdownBudget+engine.StopMargin {
		t.Errorf("stopWaitHint %s, want ShutdownBudget+StopMargin %s", stopWaitHint, engine.ShutdownBudget+engine.StopMargin)
	}
}
