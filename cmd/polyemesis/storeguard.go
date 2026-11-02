package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// shutdownBudgetSpentMsg is the one warning both shutdown paths log when the
// job tier is still running as the budget runs out.
const shutdownBudgetSpentMsg = "the background job queue had not stopped when the shutdown budget ran out; " +
	"leaving the database for the process exit to close"

// storeGuard sequences the job tier's stop against the store's close, on every
// way out of run.
//
// THE RULE IT ENFORCES: the store is closed only after the tier is down.
// sql.DB.Close closes only idle connections; one a job worker or loop is still
// querying on stays open until that query returns, so closing first left the
// database file open past the process's own cleanup (on Windows, a data
// directory that cannot be removed) and pulled the store from a worker
// mid-write. If the tier does not come down within the budget, the store is
// left for the process exit to release rather than closed under it, and the
// operator is told.
//
// Both paths go through it: the orderly shutdown calls stopTier inside the
// shared budget, and close -- deferred, so it also runs on an early error
// return -- stops the tier itself if nothing has yet. Either way the stop
// happens once.
type storeGuard struct {
	log        *slog.Logger
	closeStore func() error

	mu       sync.Mutex
	cancel   func()
	wait     func(context.Context) bool
	tierDone bool // stopTier has run to an answer
	keepOpen bool
}

func newStoreGuard(log *slog.Logger, closeStore func() error) *storeGuard {
	return &storeGuard{log: log, closeStore: closeStore}
}

// attach registers the job tier: cancel stops it, wait blocks until it is down
// or ctx ends and reports which.
func (g *storeGuard) attach(cancel func(), wait func(context.Context) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancel, g.wait = cancel, wait
}

// stopTier cancels the tier and waits for it within ctx. It reports whether the
// tier is down. On an overrun it marks the store to be left open and warns.
// Only the first call does anything.
func (g *storeGuard) stopTier(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.wait == nil || g.tierDone {
		return !g.keepOpen
	}
	g.tierDone = true
	g.cancel()
	if g.wait(ctx) {
		return true
	}
	g.keepOpen = true
	g.log.Warn(shutdownBudgetSpentMsg)
	return false
}

// close stops the tier within budget if nothing has yet, then closes the store
// unless the tier overran.
func (g *storeGuard) close(budget time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if !g.stopTier(ctx) {
		return
	}
	_ = g.closeStore()
}
