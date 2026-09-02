package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/engine"
)

/* THE LINE THAT ONLY APPEARS ON THE BAD DAY.
 *
 * Shutdown draws from one budget (engine.ShutdownBudget). If it runs out,
 * systemd SIGKILLs the cgroup and a recorder killed mid-write leaves a
 * Matroska file with no trailer at exactly the size a reader would call
 * plausible. This warning is the only thing that connects those two facts for
 * an operator reading the journal afterwards -- and a line nobody has watched
 * appear is a line nobody should rely on. #645.
 */

func capture(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestShutdownOverrunIsSaidOutLoud(t *testing.T) {
	log, buf := capture(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	warnIfShutdownOverran(ctx, log)

	out := buf.String()
	if !strings.Contains(out, "ran out of its budget") {
		t.Fatalf("nothing was logged when the shutdown budget was spent: %q", out)
	}
	// The number matters as much as the fact: an operator reading this needs to
	// know what the budget WAS to judge whether raising it is the answer.
	if !strings.Contains(out, engine.ShutdownBudget.String()) {
		t.Errorf("the warning does not name the budget it exceeded (%s): %q",
			engine.ShutdownBudget, out)
	}
}

func TestAShutdownInsideItsBudgetSaysNothing(t *testing.T) {
	// The control. A warning on every clean stop is worse than none: it trains
	// the operator to scroll past the one that matters.
	log, buf := capture(t)
	warnIfShutdownOverran(context.Background(), log)
	if buf.Len() != 0 {
		t.Errorf("a clean shutdown logged a budget warning: %q", buf.String())
	}
}
