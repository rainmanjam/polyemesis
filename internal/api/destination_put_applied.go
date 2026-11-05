package api

import "github.com/rainmanjam/polyemesis/internal/engine"

// How a PUT /destinations/{id} took effect, read back after its reconcile.
//
// E3 in the 2026-11-04 demo: saving YouTube's music-rights switch, with its
// music track already out of the mix, changed the stored profile and nothing
// the destination sends. The engine kept the process -- no new child, no new
// publish -- and the console said "is restarting with the new mix", because this
// response said nothing about what the reconcile did and the page guessed.
//
// The vocabulary is POST .../tracks/{n}/gain's (persistTrackLevels), and the
// answer is the engine's (Engine.AppliedSince), read back rather than assumed:
//
//   - "live": the destination kept the process it was running on. A level
//     reached it on the command channel, or nothing it runs from changed.
//   - "restart": this save replaced the process, with the engine's reason.
//   - absent: nothing was running to apply it to -- disabled, held, or on a
//     programme with no engine -- or the save stopped it, or the reconcile
//     failed, which `warnings` and `reconcileFailed` already say. Neither
//     "live" nor "restart" is true then.

// destinationRunBefore reads which processes a destination runs on before a
// write, and forgets the engine's last level note for it so that a note left
// by an earlier change is never reported as this one's. The engine of the
// programme it is moving to is cleared too, when the write moves it.
func (s *Server) destinationRunBefore(id int64, storedSource, nextSource *int64) engine.DestinationRun {
	var before engine.DestinationRun
	if eng := s.engineForSource(storedSource); eng != nil {
		before = eng.DestinationRunOf(id)
		eng.ForgetDestinationLive(id)
	}
	if eng := s.engineForSource(nextSource); eng != nil {
		eng.ForgetDestinationLive(id)
	}
	return before
}

// destinationApplied is how the save took effect, from the reconcile that has
// just run on the destination's (possibly new) programme. Empty when there is
// no honest answer.
func (s *Server) destinationApplied(id int64, source *int64, before engine.DestinationRun, reconcileWarn string) (applied, reason string) {
	if reconcileWarn != "" {
		return "", ""
	}
	eng := s.engineForSource(source)
	if eng == nil {
		return "", ""
	}
	return eng.AppliedSince(id, before)
}
