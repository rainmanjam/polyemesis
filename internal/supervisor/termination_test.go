package supervisor

// The termination class: the interval between asking a process to die and
// observing it dead. Four properties live here, one per mechanism.
//
//	terminate()'s escalation   -- must stop waiting when the child is reaped
//	                              (#193), and must still fire when it is not
//	runOnce()'s drain          -- must be bounded by the CHILD, not by whoever
//	                              inherited the child's pipes (#194)
//	terminate()'s REACH        -- must cover a child that is still being
//	                              spawned when the stop arrives (#126)
//
// The deadline and reap arms of stop() itself are pinned in supervisor_test.go
// by TestStopReportsWhenItHadToKillTheChild and
// TestStopReapsTheChildWhenItHasTimeTo.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------- the escalation timer

// TestTheGraceEscalatorStopsWaitingOnceTheChildIsReaped pins the cost of the
// ordinary stop -- the one that happens thousands of times more often than the
// one the escalation exists for.
//
// terminate() arms a goroutine that kills the process group if the child has not
// gone by the grace period. It used to do that with an unconditional
// time.Sleep(shutdownGrace): every stop, including the ones over in thirty
// milliseconds, left a goroutine parked for a full eight seconds, and a
// supervisor stopping forty destinations paid it forty times. The only thing
// keeping a stale one from killing an innocent successor was pointer identity on
// p.cmd -- correct, but an invariant defended by nothing, and one that could not
// be consulted until after the sleep had already been slept.
//
// WHY THE NUMBERS ARE THE NUMBERS. p.grace is raised to 60s, far above anything
// the platform can account for, so the quantity under test is a 60-second
// interval and not a scheduling artefact. The bound is 3s: twenty times the
// worst-case cost of the one process spawn and one stop this test performs on
// the slowest runner we ship to, and one twentieth of the signal. Nothing
// between those two numbers is ambiguous, so this cannot fail for being slow and
// cannot pass for being fast.
func TestTheGraceEscalatorStopsWaitingOnceTheChildIsReaped(t *testing.T) {
	const (
		grace = 60 * time.Second
		bound = 3 * time.Second
	)

	p := testProcess(t, fakeSleep(30*time.Second), Spec{})
	// Before Start: nothing else reads it yet, so no lock is owed.
	p.grace = grace
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })

	// SNAPSHOT BEFORE THE STOP, because runOnce sets p.exited to nil on teardown
	// (supervisor.go's "torn down per spawn" block). Read afterwards, a reaped
	// child and a child that never existed are the same nil, and the first
	// version of this postmortem reported "not reaped" for a reap that had
	// plainly happened. The channel itself survives being unpublished.
	p.cmdMu.Lock()
	exitedAtStart := p.exited
	p.cmdMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop on a child that honours SIGTERM returned %v after %v, want nil: this "+
			"test is about what the escalator does AFTER a clean stop, and a stop that was "+
			"not clean does not reach that question.\n%s",
			err, time.Since(started).Round(time.Millisecond), p.stopPostmortem(exitedAtStart))
	}

	// Stop has returned, so the child is reaped -- that is what the nil above
	// entails. The escalation has nothing left to escalate to, and the only
	// question is whether it knows.
	finished := make(chan struct{})
	go func() { p.escalators.Wait(); close(finished) }()

	select {
	case <-finished:
	case <-time.After(bound):
		t.Fatalf("terminate()'s grace goroutine was still running %s after Stop returned, "+
			"with the child already reaped. The grace period for this Process is %s, so "+
			"the escalator is waiting out a timer whose answer it already has: it is "+
			"sleeping the grace period unconditionally rather than selecting on the "+
			"child's exit. In production that is one goroutine per stop parked for %s, "+
			"paid per destination, holding a reference to an exec.Cmd it intends to kill.",
			bound, grace, shutdownGrace)
	}
}

// TestTheGraceEscalatorStillKillsAChildThatIgnoresTheSignal is the other side of
// the same mechanism, and it is why the test above cannot be satisfied by
// deleting the escalation.
//
// A stop whose child does not listen has exactly one thing standing between it
// and an unbounded wait, and this is it. The assertion is that Stop returns NIL:
// nil is reachable only through `case <-done:`, which entails cmd.Wait()
// returned, which entails something killed a child that ignores SIGTERM. The
// context is 10s and the grace is 300ms, so the deadline arm cannot be what
// answered -- if the escalation does not fire, this returns ErrStopDeadline.
func TestTheGraceEscalatorStillKillsAChildThatIgnoresTheSignal(t *testing.T) {
	const grace = 300 * time.Millisecond

	p := testProcess(t, fakeDeaf(60*time.Second), Spec{})
	p.grace = grace
	p.Start()
	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	// StateRunning is only "cmd.Start() returned". Deafness has to be verified or
	// this test asserts nothing: a child signalled before its handlers are
	// installed dies obediently and the escalation is never exercised.
	waitForDeaf(t, p)
	pid := p.Status().PID

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := p.Stop(ctx)

	if err != nil {
		t.Fatalf("Stop returned %v. The child ignores SIGTERM, so the ONLY thing that can "+
			"end it inside the %s deadline is terminate()'s escalation firing at the %s "+
			"grace period and killing the group. This error says it did not: the stop "+
			"fell through to its deadline arm, issued a kill it did not wait for, and "+
			"the child was still running when Stop returned.", err, 10*time.Second, grace)
	}
	if alive(pid) {
		t.Errorf("pid %d is still alive after a Stop that reported a clean stop", pid)
	}
}

// ------------------------------------------------------------- the drain bound

// TestRunOnceIsBoundedByTheChildNotByWhoeverInheritedItsPipes is #194.
//
// A pipe reaches EOF when the LAST write end closes, and a child's descendants
// inherit those write ends. runOnce used to drain stdout and stderr to EOF
// BEFORE calling cmd.Wait(), so a grandchild that outlived its parent -- FFmpeg
// spawning a helper, or anything the child forks and does not wait for -- kept
// the supervisor blocked on a process it never started and cannot name. runOnce
// never returned, supervise() never closed `done`, and every Stop on that
// Process was bounded only by the grandchild's lifetime.
//
// NO STOP IN THIS TEST, deliberately. The pathology does not need one: the harm
// is that a child which has ALREADY DIED is still reported as running, so the
// crash-loop detector, the restart policy and the monitoring page are all
// looking at a process that no longer exists. Involving Stop would also let
// terminate()'s escalation mask the bug -- it kills the process GROUP, which on
// this fixture includes the grandchild.
//
// WHY THE NUMBERS ARE THE NUMBERS. The grandchild lives 20s, which is the
// interval the broken code waits out. p.drain is lowered to 200ms so the fixed
// code's own bound is not what is being measured, and the observation bound is
// 5s -- an order of magnitude above the two process spawns this fixture costs on
// the slowest runner, and a quarter of the signal.
func TestRunOnceIsBoundedByTheChildNotByWhoeverInheritedItsPipes(t *testing.T) {
	const (
		grandchildLife = 20 * time.Second
		exitCode       = 3
		observeBound   = 5 * time.Second
	)

	rec := newRecorder()
	p := testProcess(t, fakeOrphan(grandchildLife, exitCode), Spec{
		AutoRestart: false,
		OnState:     rec.onState,
	})
	p.drain = 200 * time.Millisecond
	// Registered after testProcess, so it runs first: the grandchild is
	// re-parented to init and nothing else in this process will ever reap it.
	t.Cleanup(func() { reapOrphan(t, p) })
	p.Start()

	// The pid line is written just before the child exits, so its arrival is
	// proof that the fixture did its job: the grandchild exists and holds the
	// inherited pipes. Without this the assertion below could pass against a
	// child that failed to spawn anything.
	waitFor(t, "the child to announce the grandchild it is about to orphan", func() bool {
		for _, l := range p.Logs() {
			if strings.Contains(l.Text, orphanPIDPrefix) {
				return true
			}
		}
		return false
	})

	deadline := time.Now().Add(observeBound)
	for !rec.saw(StateFailed) {
		if time.Now().After(deadline) {
			t.Fatalf("the child exited %d %s ago and the supervisor still reports %q. It is "+
				"draining stdout and stderr to EOF BEFORE cmd.Wait(), and EOF means the "+
				"last WRITE end closed -- which the orphaned grandchild still holds, and "+
				"will hold for %s. runOnce cannot return, so supervise cannot close "+
				"`done`, so every Stop on this Process is bounded by a process the "+
				"supervisor never started. Reap the child first and bound the drain "+
				"(p.drain here is %s).",
				exitCode, observeBound, p.Status().State, grandchildLife, p.drain)
		}
		time.Sleep(time.Millisecond)
	}

	// AND THE DIAGNOSTIC SURVIVED THE BOUND. Bounding the drain is only worth
	// having if it did not buy the bound by truncating stderr: the tail of that
	// buffer is what tells a user why a destination is failing, and it is the
	// reason runOnce drained before reaping in the first place. The child's last
	// line was written before it exited, so a correct bound still captures it.
	if got := p.Status().LastError; !strings.Contains(got, orphanLastWords) {
		t.Errorf("LastError = %q, want it to contain the child's last stderr line %q. "+
			"The drain bound was paid for with the diagnostic it exists to preserve.",
			got, orphanLastWords)
	}
}

// ------------------------------------------------------- the escalation's reach

// TestAStopThatLandsWhileTheChildIsBeingSpawnedStillSignalsIt is #126's seam 6.
//
// Start() sets p.running under runMu and returns; p.cmd is not published until
// runOnce has built the command, opened its pipes and had cmd.Start() return.
// A Stop arriving between those two points takes its `p.running` arm -- so it
// does not return early -- cancels the supervise context, and calls
// terminate(), which finds p.cmd nil and returns having sent no signal and
// armed no escalator.
//
// `p.cmd == nil` MEANS TWO DIFFERENT THINGS THERE: "no child yet" and "the
// child is already reaped". terminate() could not tell them apart, and treated
// both as nothing to do. This is #290's shape -- StateStopped meaning both
// "never started" and "died" -- on the teardown path rather than the respawn
// one.
//
// The consequence is not a missed signal, it is an unsignalled child. runOnce
// uses exec.Command and not exec.CommandContext, so the cancelled context kills
// nothing: the child is spawned BEHIND a stop that has already given up on it,
// cmd.Wait() blocks on a process nobody has asked to leave, `done` never
// closes, and Stop waits out its entire deadline. Swept on the machine this was
// written on, a Stop landing 50µs to 2ms after Start reproduced it 25 times out
// of 25. On the failover tier that is a twelve-second teardown -- issue #126's
// seam 6, 12001.831ms against a 12s stopTimeout, whose predecessor's own 7.3s
// teardown pushed the next 500ms sweep to within a millisecond of the
// replacement feed's Start -- during which the outgoing feed goes on publishing
// into the very hub the replacement is about to join. For a destination it is
// worse: the SIGKILL that stop() issues on the way past is not preceded by the
// SIGTERM FFmpeg needs to finalise its output, so the recording is a header and
// nothing else.
//
// HOW THE WINDOW IS HELD OPEN. spec.NextArgs is called at the top of runOnce --
// after supervise has passed its own ctx check, before p.cmd is published --
// which is the window itself rather than a model of it. Blocking there and
// releasing after the Stop has run makes a race that is real but sub-millisecond
// into a sequence. The 250ms settle is what puts Stop past terminate(); it can
// only err by releasing too EARLY, which lets terminate() see a non-nil cmd and
// makes this test pass for the wrong reason -- and the mutation below is what
// rules that out, because a vacuous test would survive it.
//
// THE ASSERTION IS THAT Stop RETURNS NIL. nil is reachable only through
// `case <-done:`, which entails runOnce returned, which entails cmd.Wait()
// reaped a child that something asked to leave. pid != 0 is the guard against
// the other vacuous pass: a clean stop because nothing was ever spawned.
//
// Mutation: in runOnce, delete the `if ctx.Err() != nil { p.terminate() }`
// block that follows the p.cmd publish. Observed to fail on the first
// assertion -- Stop returned ErrStopDeadline after the full 3s.
func TestAStopThatLandsWhileTheChildIsBeingSpawnedStillSignalsIt(t *testing.T) {
	// Far above the cost of one spawn and one signal, far below the 60s the
	// child sleeps for, so neither arm can be reached by accident.
	const stopDeadline = 3 * time.Second

	var mu sync.Mutex
	var pid int

	f := fakeSleep(60 * time.Second)
	spawning := make(chan struct{})
	release := make(chan struct{})
	p := testProcess(t, f, Spec{
		NextArgs: func() []string {
			close(spawning)
			<-release
			return f.args
		},
		OnState: func(st Status) {
			if st.State != StateRunning || st.PID == 0 {
				return
			}
			mu.Lock()
			pid = st.PID
			mu.Unlock()
		},
	})

	p.Start()
	<-spawning // runOnce is inside the window: past its ctx check, p.cmd still nil

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopDeadline)
		defer cancel()
		stopped <- p.Stop(ctx)
	}()
	// p.running is cleared in stop()'s first critical section, so this says the
	// stop has begun; the settle after it is what carries it through cancel()
	// and terminate().
	waitFor(t, "the stop to begin", func() bool {
		p.runMu.Lock()
		defer p.runMu.Unlock()
		return !p.running
	})
	time.Sleep(250 * time.Millisecond)
	close(release)

	began := time.Now()
	err := <-stopped
	took := time.Since(began)

	if err != nil {
		t.Fatalf("Stop returned %v after %s. The stop arrived while the child was "+
			"being spawned, so terminate() found p.cmd nil and signalled nothing -- "+
			"and the child was then started behind it, unsignalled, on a context "+
			"exec.Command does not honour. Nothing ends it inside the deadline, so "+
			"Stop waits out all %s and SIGKILLs on the way past. The spawn has to "+
			"check the context once p.cmd is published and terminate itself.",
			err, took.Round(time.Millisecond), stopDeadline)
	}
	mu.Lock()
	spawnedPID := pid
	mu.Unlock()
	if spawnedPID == 0 {
		t.Fatal("no child was ever spawned, so the clean stop above proves nothing: " +
			"this test asserts that a child started INSIDE the stop window is still " +
			"signalled, and there was no such child")
	}
	if alive(spawnedPID) {
		t.Errorf("pid %d is still alive after a Stop that reported a clean stop", spawnedPID)
	}
}

// stopPostmortem says WHICH failure a missed stop was, because the three are
// different bugs and the deadline error alone cannot tell them apart.
//
// This exists because the test flaked once in CI and repetition did not find it:
// 70 iterations in isolation, 30 of the whole package under 5x CPU contention,
// none failing. The cause was eventually found by READING -- a select tie in
// stop(), fixed in the same change as this -- which is the honest account of it.
//
// The measured rate that made repetition unattractive is a JOB rate, not a test
// rate: `go build, vet, test` fails 2/70 = 2.86% of the time, and
// poly_repetitions_for puts that at ~104 CI JOB runs for 95% confidence (see
// scripts/lib-observe.sh), which is minutes each rather than the ~9ms a single
// iteration of this test costs. Cheap local repetition was worth trying and was
// tried; it is re-running the whole job a hundred times that is unaffordable.
//
// The postmortem stays because the next failure here may not be the one just
// fixed, and an absence that cannot say why it happened costs more than the
// failure it reports.
//
//	never signalled     terminate() found no cmd to signal -- the mid-spawn
//	                    reach of #126, which StateRunning is supposed to
//	                    foreclose by being set after p.cmd is published
//	signalled, alive    SIGTERM was delivered and the child did not die,
//	                    which for a fake with no handlers means the signal
//	                    went somewhere else -- look at signalGroup and pgid
//	signalled, reaped   the child DID die and `done` still did not close,
//	                    so the blockage is after the reap: the drain, or the
//	                    run loop's exit path
func (p *Process) stopPostmortem(exited <-chan struct{}) string {
	p.runMu.Lock()
	doneCh := p.done
	p.runMu.Unlock()
	done := false
	if doneCh != nil {
		select {
		case <-doneCh:
			done = true
		default:
		}
	}

	p.cmdMu.Lock()
	signalled := p.signalled
	p.cmdMu.Unlock()

	reaped := false
	if exited != nil {
		select {
		case <-exited:
			reaped = true
		default:
		}
	}
	st := p.Status()

	var verdict string
	switch {
	case !signalled:
		verdict = "NO TERM ATTEMPT RECORDED: terminate() found no cmd to reserve. That is " +
			"the mid-spawn reach of #126, which StateRunning being set after p.cmd is " +
			"published should have made unreachable here"
	case !reaped:
		// Deliberately NOT "SIGTERM was delivered". p.signalled records that
		// terminate() reserved the one allowed attempt, which it sets BEFORE
		// calling signalGroup -- so it survives a failed syscall, a blocked or
		// pending signal, and a SIGSTOPped child alike. Saying "delivered" would
		// send the next reader looking for a child that ignored a signal it may
		// never have received.
		verdict = "TERM ATTEMPT RECORDED, NOT REAPED: terminate() reserved the attempt and " +
			"the child was not reaped. That does NOT establish the signal arrived -- " +
			"p.signalled is set before signalGroup runs, and its error is discarded. " +
			"Suspect signalGroup, the process group, or a child that never got it"
	case done:
		verdict = "COMPLETED ON THE BOUNDARY: the child was reaped AND the run loop closed " +
			"`done`, so the stop finished and was reported as a timeout anyway. That is " +
			"the select tie stop() now breaks in favour of completion; seeing this again " +
			"means the tie-break regressed"
	default:
		verdict = "REAPED BUT `done` DID NOT CLOSE: the child died and the run loop did " +
			"not finish, so the blockage is after the reap -- the drain (bounded at " +
			"drainGrace) or the run loop's exit path"
	}
	return fmt.Sprintf("  postmortem: signalled=%v reaped=%v doneClosed=%v state=%q pid=%d\n  %s",
		signalled, reaped, done, st.State, st.PID, verdict)
}
