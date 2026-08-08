// Port allocation for relay subscribers.
//
// Separated from Hub because they share no state and no concept: a Hub is one
// running fan-out socket, and the allocator is a range of numbers with a set of
// the ones currently spoken for. They only ever meet in the engine, which asks
// the allocator for a port and then hands it to a Hub.
package relay

import (
	"fmt"
	"net"
	"sync"
)

// PortAllocator hands out loopback ports for subscribers.
//
// It verifies each port is actually free by binding it, which closes the
// obvious race where two subscribers are handed the same number and one of
// them silently receives the other's stream.
type PortAllocator struct {
	mu    sync.Mutex
	next  int
	base  int
	limit int
	held  map[int]bool
}

// NewPortAllocator allocates from [base, base+span).
func NewPortAllocator(base, span int) *PortAllocator {
	return &PortAllocator{next: base, base: base, limit: base + span, held: map[int]bool{}}
}

// Allocate returns a free loopback UDP port.
func (a *PortAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for tried := 0; tried < a.limit-a.base; tried++ {
		p := a.next
		a.next++
		if a.next >= a.limit {
			a.next = a.base
		}
		if a.held[p] {
			continue
		}
		if !portFree(p) {
			continue
		}
		a.held[p] = true
		return p, nil
	}
	return 0, fmt.Errorf("relay: no free UDP port in range %d-%d", a.base, a.limit-1)
}

// Release returns a port to the pool.
func (a *PortAllocator) Release(p int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.held, p)
}

func portFree(p int) bool {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
	if err != nil {
		return false
	}
	c.Close()
	return true
}
