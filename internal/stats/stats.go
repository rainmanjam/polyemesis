// Package stats keeps the short-horizon time series the monitoring page draws,
// plus host CPU/RAM sampling.
//
// Everything is in-memory and bounded. Stream telemetry is only interesting
// while it is recent, and writing a sample per second to SQLite forever would
// be a slow-motion disk leak for data nobody reads.
package stats

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
	"os"
)

// Sample is one point on the ingest bitrate graph.
type Sample struct {
	Time time.Time `json:"t"`
	// Kbps is the ingest rate over the interval preceding this sample.
	Kbps float64 `json:"kbps"`
}

// Ring is a fixed-capacity time series.
type Ring struct {
	mu   sync.RWMutex
	buf  []Sample
	next int
	full bool
}

// NewRing creates a ring holding n samples. At one sample per second, 1800
// covers the last 30 minutes the monitoring page shows.
func NewRing(n int) *Ring { return &Ring{buf: make([]Sample, n)} }

// Add appends a sample, overwriting the oldest when full.
func (r *Ring) Add(s Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Samples returns the series oldest-first.
func (r *Ring) Samples() []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.full {
		out := make([]Sample, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]Sample, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}

// System is a host resource snapshot.
type System struct {
	CPUPercent     float64 `json:"cpuPercent"`
	ProcCPUPercent float64 `json:"procCpuPercent"`
	MemUsedBytes   uint64  `json:"memUsedBytes"`
	MemTotalBytes  uint64  `json:"memTotalBytes"`
	MemPercent     float64 `json:"memPercent"`
	ProcMemBytes   uint64  `json:"procMemBytes"`
	NumCPU         int     `json:"numCpu"`
}

// Host samples this box's CPU and memory.
//
// ONE PER PROCESS, which is why it is not part of Monitor and must not be
// folded back into it. Every field it fills describes the machine and this
// process — cpu.Percent, mem.VirtualMemory and the process's own RSS — so an
// install running three programmes was running three goroutines a second
// taking three identical readings, and whichever engine an unscoped API call
// happened to reach decided which copy the operator saw. The bitrate ring on
// Monitor is the opposite case: it differentiates ONE programme's ingest
// counter and belongs to that programme.
type Host struct {
	mu     sync.RWMutex
	system System

	self *process.Process
}

// NewHost creates the host sampler. It takes no readings until Run.
func NewHost() *Host {
	h := &Host{}
	if p, err := process.NewProcess(int32(os.Getpid())); err == nil {
		h.self = p
	}
	return h
}

// Run samples until ctx is cancelled.
func (h *Host) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.sample()
		}
	}
}

func (h *Host) sample() {
	s := System{NumCPU: numCPU()}

	// Interval 0 means "since the last call", which is what makes this a
	// rolling reading rather than a blocking one-second average.
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		s.CPUPercent = pcts[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemUsedBytes = vm.Used
		s.MemTotalBytes = vm.Total
		s.MemPercent = vm.UsedPercent
	}
	if h.self != nil {
		if c, err := h.self.CPUPercent(); err == nil {
			s.ProcCPUPercent = c
		}
		if mi, err := h.self.MemoryInfo(); err == nil && mi != nil {
			s.ProcMemBytes = mi.RSS
		}
	}

	h.mu.Lock()
	h.system = s
	h.mu.Unlock()
}

// System returns the latest host snapshot. The zero value until Run has taken
// its first reading, which is the honest answer for a process that has not
// looked yet.
func (h *Host) System() System {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.system
}

// Monitor samples one programme's ingest bitrate on a fixed interval.
//
// Host resources used to be sampled here too; see Host for why they are not.
type Monitor struct {
	bitrate *Ring

	// rxBytes returns the ingest's cumulative received byte count. The monitor
	// differentiates it rather than asking for a rate, so the relay does not
	// have to keep its own timing.
	rxBytes func() uint64
}

// NewMonitor creates a monitor. rxBytes is normally relay.Hub.RxBytes.
func NewMonitor(rxBytes func() uint64) *Monitor {
	return &Monitor{
		bitrate: NewRing(1800), // 30 min at 1 Hz
		rxBytes: rxBytes,
	}
}

// Run samples until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	var lastBytes uint64
	var lastAt time.Time
	if m.rxBytes != nil {
		lastBytes = m.rxBytes()
		lastAt = time.Now()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if m.rxBytes != nil {
				cur := m.rxBytes()
				elapsed := now.Sub(lastAt).Seconds()
				var kbps float64
				if elapsed > 0 && cur >= lastBytes {
					kbps = float64(cur-lastBytes) * 8 / 1000 / elapsed
				}
				lastBytes, lastAt = cur, now
				m.bitrate.Add(Sample{Time: now, Kbps: kbps})
			}
		}
	}
}

// Bitrate returns the ingest bitrate series.
func (m *Monitor) Bitrate() []Sample { return m.bitrate.Samples() }

func numCPU() int {
	if n, err := cpu.Counts(true); err == nil {
		return n
	}
	return 0
}
