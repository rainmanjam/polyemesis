package api

// Where the broadcast-lifecycle coordinator is joined to the rest of the server.
//
// IT IS A SEPARATE FILE FROM lifecycle.go ON PURPOSE. That file promises, in its
// header and in a test that parses it, that nothing in it can reach a running
// process -- no engine, no manager, no reconcile. The escalation path needs the
// manager, because that is where the alert notifier lives. Putting the two in
// one file would make the promise unverifiable by reading, so the join lives
// here and the coordinator holds a function value instead.
//
// What crosses the boundary is one struct of strings and ids (lifecycleFault),
// travelling outward. Nothing travels back.

import (
	"context"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// lifecycleDrainBudget is how long a shutdown gives the WHOLE drain.
//
// WHOLE, NEVER PER DESTINATION, and the difference is a failed shutdown. Twelve
// destinations at ten seconds each is two minutes, which is past every service
// manager's patience: systemd sends SIGKILL, and the process dies in the middle
// of finalising a recording. One budget over the whole pass means a slow
// platform costs the destinations after it their end -- and a missed end costs a
// lingering watch page that enableAutoStop closes, which is the cheapest failure
// in the whole design.
const lifecycleDrainBudget = 10 * time.Second

// Lifecycle exposes the coordinator as the engine's observer, so main can wire
// the seam unconditionally rather than guessing when it is safe to.
//
// IT RETURNS THE INTERFACE, AND THE EARLY RETURN IS NOT DEFENSIVE PADDING. A
// Server built without a store -- which several tests in this package do -- has
// no coordinator, and `return s.lifecycle` on a nil pointer would hand the
// engine an interface value that is NOT nil, holding a nil pointer inside it.
// The engine's own nil check would pass, Wanted would be called on a nil
// receiver, and a process that had merely skipped an optional feature would
// panic on its first observe tick.
func (s *Server) Lifecycle() engine.LifecycleObserver {
	if s.lifecycle == nil {
		return nil
	}
	return s.lifecycle
}

// LifecycleLoop runs the broadcast-lifecycle sweep until ctx ends. Started
// beside RefreshLoop and PreannounceLoop.
func (s *Server) LifecycleLoop(ctx context.Context) {
	if s.lifecycle == nil {
		return
	}
	s.lifecycle.lifecycleLoop(ctx)
}

// DrainLifecycle ends what the operator has already asked to end, before the
// process goes away.
//
// It takes its own context because the caller's has usually just been cancelled:
// shutdown cancels the loops first, and a drain that inherited that context
// would do nothing at all while looking like it had run.
// DrainLifecycle drains within lifecycleDrainBudget.
//
// Kept for callers with no deadline of their own. Process shutdown must use
// DrainLifecycleWithin so this phase draws from the same budget as the rest --
// see internal/engine/shutdown_budget.go. #645.
func (s *Server) DrainLifecycle() {
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleDrainBudget)
	defer cancel()
	s.DrainLifecycleWithin(ctx)
}

// DrainLifecycleWithin drains inside the caller's deadline, or
// lifecycleDrainBudget, whichever expires first: a drain that outlived its own
// budget would eat the engines' share of the shutdown.
func (s *Server) DrainLifecycleWithin(parent context.Context) {
	if s.lifecycle == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, lifecycleDrainBudget)
	defer cancel()
	s.lifecycle.drain(ctx)
}

// escalateBroadcastFault is the only way a lifecycle fault reaches a human.
//
// THREE CHANNELS, AND NOT ONE OF THEM CAN STOP A STREAM. The row field is what
// the destination card renders, the alert is what wakes somebody, and the
// webhook is what a script hears. That is the complete response to a failed
// transition, by design: see THE RULE in lifecycle.go for why tearing the
// destination down instead would destroy the active ingest that is the only
// condition under which a retry could succeed.
func (s *Server) escalateBroadcastFault(f lifecycleFault) {
	s.publishAudit(auditBroadcastFault(f))

	s.hooks.Publish(hooks.Event{
		Trigger: hooks.TriggerBroadcastFault,
		Source:  hooks.SourceRef{ID: f.SourceID},
		Destination: &hooks.DestinationRef{
			ID: f.DestinationID, Name: f.Destination, Platform: f.Platform,
		},
		// Reason is free text and is redacted on the way in by the dispatcher,
		// like every other Reason. It carries the broadcast id because that is
		// the string somebody needs to find the thing in the platform's console;
		// it carries no token and no stream key.
		Reason: f.Detail + " (broadcast " + f.BroadcastID + ")",
	})
}

// auditBroadcastFault builds the alert.
//
// A CONSTRUCTOR RATHER THAN A STRUCT LITERAL AT THE CALL SITE, for the reason
// audit.go's constructors exist: the destination of these events is a chat
// channel that outlives the incident and gets screenshotted into tickets, and
// everyAuditEvent in audit_test.go iterates the constructors to prove the
// redactor scrubs each one. Detail is the field that earns it -- it is free text
// assembled from platform error bodies, which is the only place in this feature
// a URL could arrive from somewhere nobody chose.
func auditBroadcastFault(f lifecycleFault) alerts.Event {
	return alerts.Event{
		Type:     alerts.TypeBroadcastFault,
		Severity: alerts.SeverityWarning,
		// KEYED BY DESTINATION, not by occurrence, so a destination refusing
		// every fifteen seconds coalesces into one message instead of two
		// hundred and forty an hour. Same rule as every other alert key.
		Key:   fmt.Sprintf("broadcast.fault:%d", f.DestinationID),
		Title: fmt.Sprintf("%s: the broadcast could not be started or ended", f.Destination),
		// The stream is fine, and saying so first is the point: an operator
		// woken by this must not go and look at the encoder.
		Text: "The stream is still being delivered. What failed is the platform's own " +
			"broadcast state. " + f.Detail,
	}.WithField("platform", f.Platform).WithField("broadcast", f.BroadcastID)
}
