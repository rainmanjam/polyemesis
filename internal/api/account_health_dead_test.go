package api

import (
	"errors"
	"testing"
	"time"
)

// A refresh the platform refused for good must stop the 10-minute RefreshLoop
// retrying, until a reconnect stores a new expiry.
func TestRefreshLoopSkipsAccountsWhoseGrantIsGoneUntilReconnected(t *testing.T) {
	var b accountHealthBook
	exp := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if b.isDead(1, exp) {
		t.Fatal("an account with no failures is not dead")
	}
	b.fail(1, errors.New("transient"), time.Now())
	b.markDead(1, exp) // not called for transient failures in tokenFor; guard anyway
	if !b.isDead(1, exp) {
		t.Fatal("a refused grant at this expiry is dead")
	}
	if b.isDead(1, exp.Add(24*time.Hour)) {
		t.Fatal("a reconnect stores a new expiry and must lift the skip")
	}
	b.ok(1)
	if b.isDead(1, exp) {
		t.Fatal("a successful refresh clears it")
	}
	b.markDead(2, exp) // no recorded failure: must not invent one
	if b.isDead(2, exp) {
		t.Fatal("markDead without a failure must be a no-op")
	}
}
