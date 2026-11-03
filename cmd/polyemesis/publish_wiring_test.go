package main

import (
	"context"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/publish"
)

// The publish kind is registered, unconditionally, with the gates the caller
// supplied -- the same registration-guard trick the other wiring tests use.
func TestThePublishKindIsRegistered(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()
	q := jobs.New(log, store)
	registerPublish(log, config.Config{DataDir: dir}, store, nil, q, &publish.TokenGate{}, publish.Gates{})
	err := q.Register(publish.JobKind, 1, jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil }))
	if err == nil {
		t.Fatal("clip.publish is not registered")
	}
}
