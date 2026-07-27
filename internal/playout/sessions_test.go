package playout

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestSessionsCountOneViewerPerAddressAndVariant(t *testing.T) {
	tests := []struct {
		name string
		obs  [][2]string // {address, variant}
		want int
	}{
		{"a single viewer polling repeatedly is one viewer",
			[][2]string{{"10.0.0.1", "hd"}, {"10.0.0.1", "hd"}, {"10.0.0.1", "hd"}}, 1},
		{"different addresses are different viewers",
			[][2]string{{"10.0.0.1", "hd"}, {"10.0.0.2", "hd"}}, 2},
		{"one viewer watching two rungs is counted on each",
			[][2]string{{"10.0.0.1", "hd"}, {"10.0.0.1", "sd"}}, 2},
		{"an unreadable address still counts rather than vanishing",
			[][2]string{{"", "hd"}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessions(20*time.Second, 100)
			for _, o := range tc.obs {
				s.Observe(o[0], o[1], RequestPlaylist, epoch)
			}
			if got := s.Snapshot(epoch).Viewers; got != tc.want {
				t.Fatalf("viewers = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSessionsExpireAfterTheIdleWindow(t *testing.T) {
	tests := []struct {
		name  string
		after time.Duration
		want  int
	}{
		{"still counted inside the window", 19 * time.Second, 1},
		{"dropped exactly at the window", 20 * time.Second, 0},
		{"long gone well past it", time.Hour, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessions(20*time.Second, 100)
			s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
			if got := s.Snapshot(epoch.Add(tc.after)).Viewers; got != tc.want {
				t.Fatalf("viewers after %s = %d, want %d", tc.after, got, tc.want)
			}
		})
	}
}

func TestSegmentRequestsKeepAViewerAlive(t *testing.T) {
	// A viewer seeking inside a DVR window pulls segments off a manifest they
	// already hold. Counting only playlist polls would report an empty stream
	// while people were watching it.
	s := NewSessions(20*time.Second, 100)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	s.Observe("10.0.0.1", "hd", RequestSegment, epoch.Add(15*time.Second))

	if got := s.Snapshot(epoch.Add(25 * time.Second)).Viewers; got != 1 {
		t.Fatalf("viewers = %d, want 1: the segment request should have refreshed the session", got)
	}
}

func TestAViewerWhoComesBackCountsAsANewSession(t *testing.T) {
	s := NewSessions(20*time.Second, 100)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	// Past the window but before any sweep has run, so the stale entry is still
	// in the table: it must be recognised as a reconnect, not a continuation.
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch.Add(time.Hour))

	a := s.Snapshot(epoch.Add(time.Hour))
	if a.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", a.Sessions)
	}
	if a.Viewers != 1 {
		t.Fatalf("viewers = %d, want 1", a.Viewers)
	}
}

func TestSessionsAreBoundedAndNeverRefuseService(t *testing.T) {
	s := NewSessions(20*time.Second, 3)
	for i := 0; i < 50; i++ {
		s.Observe(fmt.Sprintf("10.0.0.%d", i), "hd", RequestPlaylist, epoch)
	}
	a := s.Snapshot(epoch)

	if a.Viewers != 3 {
		t.Fatalf("viewers = %d, want the cap of 3", a.Viewers)
	}
	if a.Uncounted == 0 {
		t.Fatal("uncounted = 0; a full table must say so rather than silently under-report")
	}
	// Every request was still observed, which is the point: accounting is never
	// the reason a stream stops playing.
	if a.Requests != 50 {
		t.Fatalf("requests = %d, want 50", a.Requests)
	}
}

func TestAFullTableReclaimsExpiredEntriesBeforeTurningAViewerAway(t *testing.T) {
	s := NewSessions(20*time.Second, 2)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	s.Observe("10.0.0.2", "hd", RequestPlaylist, epoch)

	later := epoch.Add(time.Hour)
	s.Observe("10.0.0.3", "hd", RequestPlaylist, later)

	a := s.Snapshot(later)
	if a.Viewers != 1 {
		t.Fatalf("viewers = %d, want 1", a.Viewers)
	}
	if a.Uncounted != 0 {
		t.Fatalf("uncounted = %d; the two expired entries should have made room", a.Uncounted)
	}
}

func TestPeakIsAHighWaterMarkAndDoesNotDecay(t *testing.T) {
	s := NewSessions(20*time.Second, 100)
	for i := 0; i < 5; i++ {
		s.Observe(fmt.Sprintf("10.0.0.%d", i), "hd", RequestPlaylist, epoch)
	}
	if got := s.Snapshot(epoch).Peak; got != 5 {
		t.Fatalf("peak = %d, want 5", got)
	}

	a := s.Snapshot(epoch.Add(time.Hour))
	if a.Viewers != 0 {
		t.Fatalf("viewers = %d, want 0", a.Viewers)
	}
	if a.Peak != 5 {
		t.Fatalf("peak = %d, want it to survive the viewers leaving", a.Peak)
	}
	if a.PeakAt == nil || !a.PeakAt.Equal(epoch) {
		t.Fatalf("peakAt = %v, want %v", a.PeakAt, epoch)
	}
}

func TestByVariantSplitsTheLadder(t *testing.T) {
	s := NewSessions(20*time.Second, 100)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	s.Observe("10.0.0.2", "hd", RequestPlaylist, epoch)
	s.Observe("10.0.0.3", "sd", RequestSegment, epoch)

	a := s.Snapshot(epoch)
	if a.ByVariant["hd"] != 2 || a.ByVariant["sd"] != 1 {
		t.Fatalf("byVariant = %v, want hd=2 sd=1", a.ByVariant)
	}
	if got := s.Variants(epoch); len(got) != 2 || got[0] != "hd" {
		t.Fatalf("variants = %v, want the busiest rung first", got)
	}
}

func TestSettingsChangeDoesNotDropLiveViewers(t *testing.T) {
	s := NewSessions(20*time.Second, 100)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	s.SetLimits(60*time.Second, 200)

	a := s.Snapshot(epoch.Add(30 * time.Second))
	if a.Viewers != 1 {
		t.Fatalf("viewers = %d, want the widened window to keep the viewer", a.Viewers)
	}
	if a.IdleSeconds != 60 || a.Capacity != 200 {
		t.Fatalf("limits = %ds/%d, want 60s/200", a.IdleSeconds, a.Capacity)
	}
}

func TestResetClearsTheTableAndTheCounters(t *testing.T) {
	s := NewSessions(20*time.Second, 100)
	s.Observe("10.0.0.1", "hd", RequestPlaylist, epoch)
	s.Reset()

	a := s.Snapshot(epoch)
	if a.Viewers != 0 || a.Peak != 0 || a.Sessions != 0 || a.Requests != 0 || a.PeakAt != nil {
		t.Fatalf("after reset: %+v, want everything zeroed", a)
	}
}

func TestSessionsAreSafeUnderConcurrentRequests(t *testing.T) {
	s := NewSessions(20*time.Second, 1000)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s.Observe(fmt.Sprintf("10.0.%d.%d", i, j%7), "hd", RequestSegment, epoch)
				if j%20 == 0 {
					s.Snapshot(epoch)
				}
			}
		}(i)
	}
	wg.Wait()

	if got := s.Snapshot(epoch).Viewers; got != 16*7 {
		t.Fatalf("viewers = %d, want %d", got, 16*7)
	}
}
