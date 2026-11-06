package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/api"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/publish"
)

// Every kind the jobs page lists must have a worker behind it, and the queue
// refuses a second registration, so a duplicate attempt is the observable.
func TestEveryCataloguedJobKindIsRegistered(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()
	q := jobs.New(log, store)
	gov := jobs.NewGovernor(log, q)
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-ffmpeg"), FFprobe: filepath.Join(dir, "no-ffprobe")}
	cfg := config.Config{DataDir: dir}
	registerProcessors(log, cfg, store, nil, gov, q, tools, nil, db.PostProdSettings{})
	registerPublish(log, cfg, store, nil, q, &publish.TokenGate{}, publish.Gates{})

	noop := jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil })
	for _, k := range api.CataloguedKinds() {
		if q.Register(k, 1, noop) == nil {
			t.Errorf("kind %q is on the jobs page but nothing registered a worker for it", k)
		}
	}
}
