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

// Monitor samples host resources and the ingest bitrate on a fixed interval.
type Monitor struct {
	mu      sync.RWMutex
	system  System
	bitrate *Ring

	// rxBytes returns the ingest's cumulative received byte count. The monitor
	// differentiates it rather than asking for a rate, so the relay does not
	// have to keep its own timing.
	rxBytes func() uint64

	self *process.Process
}

// NewMonitor creates a monitor. rxBytes is normally relay.Hub.RxBytes.
func NewMonitor(rxBytes func() uint64) *Monitor {
	m := &Monitor{
		bitrate: NewRing(1800), // 30 min at 1 Hz
		rxBytes: rxBytes,
	}
	if p, err := process.NewProcess(int32(os.Getpid())); err == nil {
		m.self = p
	}
	return m
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
			m.sampleSystem()
		}
	}
}

func (m *Monitor) sampleSystem() {
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
	if m.self != nil {
		if c, err := m.self.CPUPercent(); err == nil {
			s.ProcCPUPercent = c
		}
		if mi, err := m.self.MemoryInfo(); err == nil && mi != nil {
			s.ProcMemBytes = mi.RSS
		}
	}

	m.mu.Lock()
	m.system = s
	m.mu.Unlock()
}

// System returns the latest host snapshot.
func (m *Monitor) System() System {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.system
}

// Bitrate returns the ingest bitrate series.
func (m *Monitor) Bitrate() []Sample { return m.bitrate.Samples() }

func numCPU() int {
	if n, err := cpu.Counts(true); err == nil {
		return n
	}
	return 0
}
