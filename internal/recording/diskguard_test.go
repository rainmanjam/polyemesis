package recording

import (
	"errors"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// volume fakes a filesystem whose free space the test moves at will.
type volume struct {
	free  uint64
	total uint64
	err   error
}

func (v *volume) statfs(string) (uint64, uint64, error) { return v.free, v.total, v.err }

func gb(n float64) uint64 { return uint64(n * bytesPerGB) }

func guardManager(t *testing.T, v *volume) *Manager {
	t.Helper()
	m, _, _ := newManager(t)
	m.freeSpace = v.statfs
	return m
}

func floorAt(minFreeGB float64) db.RecordingSettings {
	return db.RecordingSettings{Enabled: true, SegmentSeconds: 3600, MinFreeGB: minFreeGB}
}

func TestFreeSpaceFloorHaltsAndResumesWithHysteresis(t *testing.T) {
	tests := []struct {
		name        string
		freeGB      float64
		startHalted bool
		wantHalted  bool
	}{
		{"below the floor halts recording", 1, false, true},
		{"exactly at the floor keeps recording", 5, false, false},
		{"well above the floor keeps recording", 50, false, false},
		{"halted stays halted at the bare floor", 5, true, true},
		{"halted stays halted just under the resume margin", 6.1, true, true},
		{"halted resumes once free space clears the margin", 6.25, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &volume{free: gb(tt.freeGB), total: gb(100)}
			m := guardManager(t, v)
			if tt.startHalted {
				m.setStorage(StorageState{Halted: true, Reason: "seeded"})
			}
			m.CheckFreeSpace(floorAt(5))

			if got := m.Storage().Halted; got != tt.wantHalted {
				t.Errorf("halted = %v with %.2f GB free and a 5 GB floor, want %v",
					got, tt.freeGB, tt.wantHalted)
			}
			if m.RecordingAllowed() == tt.wantHalted {
				t.Errorf("RecordingAllowed() = %v contradicts halted = %v",
					m.RecordingAllowed(), tt.wantHalted)
			}
		})
	}
}

func TestHaltReasonNamesTheNumbersAnOperatorNeeds(t *testing.T) {
	m := guardManager(t, &volume{free: gb(1.5), total: gb(100)})
	m.CheckFreeSpace(floorAt(5))

	got := m.Storage().Reason
	if got == "" {
		t.Fatal("a halt must carry a reason the UI can show")
	}
	for _, want := range []string{"1.5", "5.0", m.dir} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q omits %q", got, want)
		}
	}
}

func TestGuardStaysOutOfTheWayWhenItCannotJudge(t *testing.T) {
	tests := []struct {
		name string
		v    *volume
		s    db.RecordingSettings
	}{
		{"a zero floor disables the guard", &volume{free: 0, total: gb(100)}, floorAt(0)},
		{"a negative floor disables the guard", &volume{free: 0, total: gb(100)}, floorAt(-1)},
		{"an unreadable volume is not evidence of a full one",
			&volume{err: errors.New("statfs: permission denied")}, floorAt(5)},
		{"a platform without statfs reports zeroes, not a full disk",
			&volume{free: 0, total: 0}, floorAt(5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := guardManager(t, tt.v)
			m.CheckFreeSpace(tt.s)
			if m.Storage().Halted {
				t.Errorf("halted recording on %s", tt.name)
			}
		})
	}
}

func TestClearingTheFloorLiftsAHaltAlreadyInPlace(t *testing.T) {
	m := guardManager(t, &volume{free: gb(1), total: gb(100)})
	m.CheckFreeSpace(floorAt(5))
	if !m.Storage().Halted {
		t.Fatal("setup: expected the guard to halt")
	}

	m.CheckFreeSpace(floorAt(0))
	if m.Storage().Halted {
		t.Error("turning the floor off must release a halt it caused")
	}
}

func TestStorageGuardCallbackFiresOnlyOnTransitions(t *testing.T) {
	var seen []StorageState
	m, _, _ := newManagerIn(t, t.TempDir())
	v := &volume{free: gb(1), total: gb(100)}
	m.freeSpace = v.statfs
	m.onStorage = func(st StorageState) { seen = append(seen, st) }

	for i := 0; i < 3; i++ {
		m.CheckFreeSpace(floorAt(5))
	}
	v.free = gb(50)
	for i := 0; i < 3; i++ {
		m.CheckFreeSpace(floorAt(5))
	}

	if len(seen) != 2 {
		t.Fatalf("callback fired %d times across two transitions: %+v", len(seen), seen)
	}
	if !seen[0].Halted || seen[1].Halted {
		t.Errorf("expected halt then resume, got %+v", seen)
	}
}

func TestCheckFreeSpaceReportsWhetherTheVerdictChanged(t *testing.T) {
	m := guardManager(t, &volume{free: gb(1), total: gb(100)})
	if !m.CheckFreeSpace(floorAt(5)) {
		t.Fatal("the first halt is a change and must be reported")
	}
	if m.CheckFreeSpace(floorAt(5)) {
		t.Error("staying halted is not a change; reporting one would spam the event bus")
	}
}

func TestSweepTickAppliesTheFloorAndSurfacesItInUsage(t *testing.T) {
	m := guardManager(t, &volume{free: gb(1), total: gb(100)})
	m.ScanAndSweep(floorAt(5))

	u, err := m.Usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !u.Storage.Halted || u.Storage.Reason == "" {
		t.Errorf("usage must carry the halt so the UI can explain it; got %+v", u.Storage)
	}
}
