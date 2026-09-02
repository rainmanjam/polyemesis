package api

import (
	"fmt"
	"net/http"
)

// GET /api/v1/health USED TO BE A CONSTANT, AND THREE MECHANISMS TREAT IT AS
// PROOF.
//
// scripts/install.sh polls it to decide the install worked, the container
// images bake it into HEALTHCHECK, and it is the obvious thing for an operator
// to point monitoring at. What it returned was a literal `{"status":"ok"}`
// written by a closure in the route table -- a statement that this process is
// running and chi is routing, which is exactly the failure mode nobody needs
// help detecting. A server whose database had gone away, whose engines had all
// failed to build, and whose recording volume was full answered it identically.
//
// It now reports three things that can be false.
//
// TWO TIERS, AND THE SPLIT IS NOT COSMETIC. The status code is what a container
// orchestrator acts on, and acting means restarting a process that may be
// publishing live video. So only the conditions a restart could plausibly
// address answer 503: the database not reading, and an install with sources
// configured and not one engine running. A recording volume below its floor is
// real and is reported -- but a restart does not add disk, and taking a live
// programme off the air over it would be this endpoint causing the outage it
// exists to detect. That is "degraded" with a 200, and monitoring keys off the
// status field rather than the code.
//
// A HEALTHY ANSWER IS BYTE-IDENTICAL TO THE OLD ONE: `{"status":"ok"}` and
// nothing else. Existing callers compare that exact string
// (scripts/acceptance-tls.sh does, three times), and a richer body on the happy
// path would break them to say something none of them read.
//
// STILL UNAUTHENTICATED AND STILL CHEAP. One COUNT on a small table, one map
// read under the manager's lock, and one field off a struct the recording sweep
// already maintains -- no statfs here, the free-space guard does that on its
// own tick and this reports its verdict. Nothing below discloses anything an
// anonymous caller could not already learn from whether the port answers, which
// is why it stays off the authenticated group: a monitor that needs a
// credential is a monitor that stops working when the credential is rotated.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
	}
	checks := make([]check, 0, 3)
	fatal := false

	// The database. Not "is the handle non-nil" -- a query, because the failure
	// this catches is a file that has gone away, a volume unmounted under a
	// running process, or a handle that will not give up a connection.
	dbCheck := check{Name: "database", OK: true}
	if err := s.store.Ping(); err != nil {
		dbCheck.OK, dbCheck.Detail, fatal = false, err.Error(), true
	}
	checks = append(checks, dbCheck)

	// The engine set. SOURCES CONFIGURED AND NOT ONE ENGINE RUNNING is the same
	// condition Manager.Start refuses to boot on, and Sync can produce it later
	// on a install that booted fine. No sources at all is a fresh install and a
	// normal state, not a failure -- see Manager.Start.
	eng := check{Name: "engine", OK: true}
	switch {
	case s.mgr == nil:
		// A build with no engine wired: nothing to be reachable or not. Said
		// out loud rather than passed silently, so a fixture cannot be mistaken
		// for a healthy server.
		eng.Detail = "no engine manager in this process"
	default:
		running := len(s.mgr.Engines())
		sources, err := s.store.CountSources()
		switch {
		case err != nil:
			eng.OK, eng.Detail, fatal = false, "cannot count sources: "+err.Error(), true
		case sources > 0 && running == 0:
			eng.OK, fatal = false, true
			eng.Detail = fmt.Sprintf("%d source(s) configured and no engine running; nothing is being published", sources)
		default:
			eng.Detail = fmt.Sprintf("%d of %d source(s) running", running, sources)
		}
	}
	checks = append(checks, eng)

	// The recording floor. Reported, never fatal: see the two-tier note above.
	disk := check{Name: "recordingDisk", OK: true}
	if st := s.storageVerdict(); st.Halted {
		disk.OK = false
		disk.Detail = st.Reason
		if disk.Detail == "" {
			disk.Detail = "recording is halted by the free-space floor"
		}
	}
	checks = append(checks, disk)

	degraded := false
	for _, c := range checks {
		if !c.OK {
			degraded = true
		}
	}
	if !degraded {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	status, code := "degraded", http.StatusOK
	if fatal {
		status, code = "unhealthy", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": checks})
}
