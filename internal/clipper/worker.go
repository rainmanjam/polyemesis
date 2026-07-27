package clipper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// A clip export belongs on the job queue, not on an HTTP request.
//
// A fast cut of a forty-minute segment moves gigabytes and a precise one runs
// an encoder, and both can arrive while a broadcast is going out. The queue is
// where the resource policy lives, so this is the adapter that puts the cutter
// behind it — and there is no second scheduler here, no timer, no goroutine
// that outlives a call. The governor decides WHEN; this file only knows HOW.

// JobKind is what a clip export is registered as. Every caller must use this
// constant: the queue folds duplicate submissions on Kind and Target, and two
// spellings would mean pressing Export twice cuts the clip twice.
const JobKind jobs.Kind = "clip.export"

// JobParams is the JSON a clip job carries.
type JobParams struct {
	Request Request `json:"request"`
	// Segments is the timeline to cut from. Optional: a job that omits it is
	// resolved from its Target instead, which is how a queued job survives the
	// recording being re-indexed between submission and execution.
	Segments []Segment `json:"segments,omitempty"`

	// EDLPath, when set, writes an OpenTimelineIO sidecar next to the clip.
	EDLPath string `json:"edlPath,omitempty"`
	// EDLRate is the recording's frame rate. See DefaultEDLRate for what a zero
	// costs.
	EDLRate float64 `json:"edlRate,omitempty"`
}

// JobResult is what the worker hands back to the queue.
type JobResult struct {
	Path    string  `json:"path"`
	Bytes   int64   `json:"bytes"`
	Mode    Mode    `json:"mode"`
	Seconds float64 `json:"seconds"`
	// InDriftMS and DriftKnown are the whole reason this result is not just a
	// path: whatever consumes it has to be able to tell the user their in-point
	// moved, hours after they stopped watching for it.
	InDriftMS   int64    `json:"inDriftMs"`
	DriftKnown  bool     `json:"driftKnown"`
	ReEncodedMS int64    `json:"reEncodedMs"`
	Sources     int      `json:"sources"`
	EDLPath     string   `json:"edlPath,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Resolver turns a job's Target into the segments to cut from.
//
// A callback rather than a database handle, so this package never learns what a
// recording row looks like and the queue wiring stays in the layer that already
// knows both.
type Resolver func(ctx context.Context, target string) (Timeline, error)

// NewWorker adapts a Cutter to the job queue.
func NewWorker(c *Cutter, resolve Resolver) jobs.Worker {
	return jobs.WorkerFunc(func(ctx context.Context, job jobs.Job, rep jobs.Reporter) error {
		params, err := parseJobParams(job)
		if err != nil {
			return err
		}
		tl, err := timelineFor(ctx, params, job.Target, resolve)
		if err != nil {
			return jobs.Permanent(err)
		}
		plan, err := planForJob(ctx, c, tl, params.Request)
		if err != nil {
			return err
		}
		// The plan goes into the log BEFORE the work starts, so a job somebody
		// checks on tomorrow still says how far the in-point moved even if the
		// cut itself later failed.
		rep.Logf("%s", plan.Describe())
		for _, w := range plan.Warnings {
			rep.Logf("%s", w)
		}

		res, err := c.Execute(ctx, plan, jobProgress(rep))
		if err != nil {
			return err
		}
		rep.SetResult(jobResult(plan, res, params, rep))
		return nil
	})
}

// parseJobParams reads a job's payload. A payload that does not parse is
// permanent: no retry will make the same bytes into different JSON.
func parseJobParams(job jobs.Job) (JobParams, error) {
	var params JobParams
	if len(job.Params) == 0 {
		return params, nil
	}
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return params, jobs.Permanent(fmt.Errorf("clipper: job params: %w", err))
	}
	return params, nil
}

// planForJob plans, marking the failures a retry cannot help.
func planForJob(ctx context.Context, c *Cutter, tl Timeline, req Request) (Plan, error) {
	plan, err := c.Plan(ctx, tl, req)
	if err != nil && permanentPlanError(err) {
		return Plan{}, jobs.Permanent(err)
	}
	return plan, err
}

// jobProgress bridges the cutter's progress callback to the queue's reporter.
func jobProgress(rep jobs.Reporter) ProgressFunc {
	return func(f float64, msg string) {
		rep.Progress(f)
		// "done" is the queue's own word for a finished job; repeating it in the
		// log tail would read as a second thing having happened.
		if msg != "" && msg != "done" {
			rep.Logf("%s", msg)
		}
	}
}

// jobResult summarises a finished cut and writes the optional EDL sidecar.
func jobResult(plan Plan, res Result, params JobParams, rep jobs.Reporter) JobResult {
	out := JobResult{
		Path:        res.Path,
		Bytes:       res.Bytes,
		Mode:        plan.Mode,
		Seconds:     plan.Duration().Seconds(),
		InDriftMS:   plan.InDrift.Milliseconds(),
		DriftKnown:  plan.DriftKnown,
		ReEncodedMS: plan.HeadDuration.Milliseconds(),
		Sources:     len(plan.Sources),
		Warnings:    plan.Warnings,
	}
	if params.EDLPath == "" {
		return out
	}
	if err := WriteOTIO(params.EDLPath, FromPlan(plan, params.EDLRate)); err != nil {
		// The clip is already published and correct. Losing the sidecar must
		// not fail the job that produced it.
		rep.Logf("the clip was written but its EDL was not: %v", err)
		return out
	}
	out.EDLPath = params.EDLPath
	return out
}

// timelineFor prefers the segments the job carries and falls back to resolving
// its target.
func timelineFor(ctx context.Context, params JobParams, target string, resolve Resolver) (Timeline, error) {
	if len(params.Segments) > 0 {
		return NewTimeline(params.Segments)
	}
	if resolve == nil {
		return Timeline{}, fmt.Errorf("clipper: job %q carries no segments and there is no resolver", target)
	}
	return resolve(ctx, target)
}

// permanentPlanError separates "this request can never work" from "this attempt
// did not work". Only the first is worth refusing to retry: a probe that timed
// out under load is exactly the case a retry exists for.
func permanentPlanError(err error) bool {
	return errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrEmptyRange) ||
		errors.Is(err, ErrOutOfRange) ||
		errors.Is(err, ErrNoSegments) ||
		errors.Is(err, ErrNoWorkDir)
}
