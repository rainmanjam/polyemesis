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
// So each engine keeps judging it, and the gate passes a transition only
// when it changes what the install has been told: the first disk.low while
// the install was last told "recovered" (or nothing), and the first
// disk.recovered after that. Every other engine's copy of the same edge is
// the same news and is dropped.
//
// A new engine born while the disk is already low fires disk.low from its
// fresh watcher and is dropped for the same reason, which is the other half
// of "once per install": adding a programme is not news about the disk.
//
// A nil gate admits everything. That is an engine assembled field by field in
// a test, and it keeps that engine's old behaviour.
type InstallGate struct {
	mu   sync.Mutex
	last map[string]Type
}

// NewInstallGate returns a gate that has told the install nothing yet.
func NewInstallGate() *InstallGate {
	return &InstallGate{last: map[string]Type{}}
}

// Admit reports whether ev should be published. Programme-scoped events are
// always admitted; the gate only arbitrates the install's own subjects.
func (g *InstallGate) Admit(ev Event) bool {
	if g == nil || !ev.Type.InstallScoped() {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last[ev.Key] == ev.Type {
		return false
	}
	g.last[ev.Key] = ev.Type
	return true
}
