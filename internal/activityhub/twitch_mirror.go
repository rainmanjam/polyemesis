package activityhub

import (
	"context"
	"sync"
	"time"
)

// MirrorLedger records the native Twitch polls polyemesis itself created when
// it mirrored a chat poll (internal/api/polls.go), so the EventSub source does
// not report the same poll twice: once as the chat poll polyemesis ran and once
// as a native poll Twitch announces (decision D9).
//
// The ids alone are not enough. Twitch can announce channel.poll.begin before
// the Create Poll call that made it has returned its id, so Begin marks a
// create as in flight and Mirrored waits (bounded) for the in-flight ones to
// finish before it answers. The zero value is ready to use.
type MirrorLedger struct {
	mu      sync.Mutex
	ids     map[string]struct{}
	order   []string
	pending int
	changed chan struct{}
}

// mirrorLedgerCap bounds the remembered ids; a stream makes a handful of polls.
const mirrorLedgerCap = 256

// Begin marks a native-poll create as in flight and returns the function that
// finishes it. Call the function exactly once, with the poll's id or "" when
// the create failed.
func (m *MirrorLedger) Begin() (done func(pollID string)) {
	if m == nil {
		return func(string) {}
	}
	m.mu.Lock()
	m.pending++
	m.mu.Unlock()
	var once sync.Once
	return func(id string) {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.pending--
			if id != "" {
				if m.ids == nil {
					m.ids = map[string]struct{}{}
				}
				if _, ok := m.ids[id]; !ok {
					m.ids[id] = struct{}{}
					m.order = append(m.order, id)
					if len(m.order) > mirrorLedgerCap {
						delete(m.ids, m.order[0])
						m.order = m.order[1:]
					}
				}
			}
			if m.changed != nil {
				close(m.changed)
				m.changed = nil
			}
		})
	}
}

// Mirrored reports whether polyemesis created the native poll id. While a
// create is in flight it waits up to wait for it to finish first.
func (m *MirrorLedger) Mirrored(ctx context.Context, id string, wait time.Duration) bool {
	if m == nil || id == "" {
		return false
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		m.mu.Lock()
		if _, ok := m.ids[id]; ok {
			m.mu.Unlock()
			return true
		}
		if m.pending == 0 {
			m.mu.Unlock()
			return false
		}
		if m.changed == nil {
			m.changed = make(chan struct{})
		}
		ch := m.changed
		m.mu.Unlock()
		select {
		case <-ch:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}
