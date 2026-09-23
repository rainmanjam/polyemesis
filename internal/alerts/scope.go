package alerts

import "sync"

// SourceRef is the programme a watcher speaks for. Named the way
// hooks.SourceRef is, which this package cannot import: hooks imports alerts.
type SourceRef struct {
	ID   int64
	Name string
}

// InstallScoped reports whether t describes the whole install rather than one
// programme.
//
// Only the recording volume, today. Every engine measures it, because every
// engine records to cfg.RecordingsDir(), and every engine has its own
// notifier -- so a volume filling up on a three-programme install was three
// disk.low deliveries through each rule, identical but for their timestamps.
// These events carry no programme, and InstallGate lets one of them through.
func (t Type) InstallScoped() bool {
	return t == TypeDiskLow || t == TypeDiskRecovered
}

// InstallGate lets ONE engine speak for the install on an install-scoped
// subject.
//
// The engines cannot agree among themselves without something shared, and a
// watcher on the manager instead would need a notifier of its own and a
// snapshot nobody builds; the engines already measure the disk every sweep.
// So each engine keeps judging it, and the gate tracks WHICH LIVE ENGINES
// currently hold the subject low. disk.low is admitted when it is the first
// holder -- the install has not been told, or has been told "recovered" --
// and disk.recovered when the last holder lets go. Every other engine's copy
// of the same edge is the same news and is dropped.
//
// A new engine born while the disk is already low fires disk.low from its
// fresh watcher and is dropped for the same reason, which is the other half
// of "once per install": adding a programme is not news about the disk.
//
// One disk.low is admitted although another engine holds the subject: one
// MORE SEVERE than any the install has been told. The first copy may have
// been the warning ("2 GB free") and a later engine's the critical
// ("Recording has been stopped"); dropping that would mean a rule floored at
// critical never hears that recording halted.
//
// And a holder that goes away -- its programme deleted, see Release -- takes
// its claim with it, so a later disk.low from an engine alive now is not
// swallowed by the memory of one that is gone. Remembering "told low" for the
// life of the process was the bug this shape replaced: every holder deleted,
// the disk freed, and the next fill -- critical form included -- said nothing
// until restart.
//
// A nil gate admits everything. That is an engine assembled field by field in
// a test, and it keeps that engine's old behaviour.
type InstallGate struct {
	mu   sync.Mutex
	subj map[string]*gateSubject
}

// gateSubject is one install-scoped key: who holds it low, and the most
// severe low the install has been told while they have.
type gateSubject struct {
	holders map[any]struct{}
	told    Severity
}

// NewInstallGate returns a gate that has told the install nothing yet.
func NewInstallGate() *InstallGate {
	return &InstallGate{subj: map[string]*gateSubject{}}
}

// Admit reports whether ev, observed by holder, should be published. holder
// is whatever identifies one watcher for its lifetime -- the engine -- and is
// what Release later names. Programme-scoped events are always admitted; the
// gate only arbitrates the install's own subjects.
func (g *InstallGate) Admit(holder any, ev Event) bool {
	if g == nil || !ev.Type.InstallScoped() {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.subj[ev.Key]
	if s == nil {
		s = &gateSubject{holders: map[any]struct{}{}}
		g.subj[ev.Key] = s
	}
	if ev.Type == TypeDiskRecovered {
		if _, held := s.holders[holder]; !held {
			return false
		}
		delete(s.holders, holder)
		if len(s.holders) > 0 {
			return false
		}
		s.told = ""
		return true
	}
	// A low. Held whether or not it is admitted: the engine's watcher has
	// fired and will report the recovery, and until it does the install is
	// not recovered.
	first := len(s.holders) == 0
	s.holders[holder] = struct{}{}
	if first || ev.Severity.rank() > s.told.rank() {
		s.told = ev.Severity
		return true
	}
	return false
}

// Release forgets holder: its engine has stopped, and its watcher will never
// report the recovery. Silent -- the disk has not recovered because a
// programme was deleted -- but once no live holder remains, the next disk.low
// is news again. Nil-safe, like Admit.
func (g *InstallGate) Release(holder any) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.subj {
		delete(s.holders, holder)
		if len(s.holders) == 0 {
			s.told = ""
		}
	}
}
