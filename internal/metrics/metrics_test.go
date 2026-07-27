package metrics

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

func testSnapshot() Snapshot {
	return Snapshot{
		Version: "1.2.3",
		Uptime:  90 * time.Second,
		Ingest: Process{
			State:       "running",
			Restarts:    2,
			BitrateKbps: 6000,
		},
		Destinations: []Destination{
			{
				Process:  Process{State: "reconnecting", Restarts: 7, BitrateKbps: 4500, DropFrames: 12},
				ID:       9,
				Name:     "Twitch",
				Kind:     "rtmp",
				Platform: "twitch",
				Enabled:  true,
			},
			{
				Process:  Process{State: "stopped"},
				ID:       3,
				Name:     "Archive",
				Kind:     "file",
				Platform: "custom",
			},
		},
		Relay:      Relay{Subscribers: 4, RxPackets: 100, RxBytes: 131600, TxPackets: 400, Dropped: 5},
		Recordings: Recordings{Files: 6, UsedBytes: 1 << 30, FreeBytes: 1 << 40, TotalBytes: 2 << 40},
		Host: Host{
			CPUPercent: 41.5, MemUsedBytes: 1024, MemTotalBytes: 4096,
			ProcCPUPercent: 12.5, ProcMemBytes: 512, NumCPU: 8,
		},
	}
}

// parse turns exposition text into a lookup keyed by the full series
// identifier, i.e. the whole line up to the value.
func parse(t *testing.T, text string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		series, value, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("sample line has no value: %q", line)
		}
		if _, dup := out[series]; dup {
			t.Errorf("duplicate series %q", series)
		}
		out[series] = value
	}
	return out
}

func TestSnapshotIsExposedAsPrometheusSamples(t *testing.T) {
	got := parse(t, Render(testSnapshot()))

	tests := []struct {
		name   string
		series string
		want   string
	}{
		{"version is carried on build_info", `polyemesis_build_info{version="1.2.3"}`, "1"},
		{"uptime is in seconds", "polyemesis_uptime_seconds", "90"},

		{"ingest is up while running", "polyemesis_ingest_up", "1"},
		{"ingest kbps is exposed as bits per second", "polyemesis_ingest_bitrate_bits_per_second", "6e+06"},
		{"ingest restarts are counted", "polyemesis_ingest_restarts_total", "2"},

		{"destination labels are on info", `polyemesis_destination_info{id="9",name="Twitch",kind="rtmp",platform="twitch"}`, "1"},
		{"a running destination is enabled", `polyemesis_destination_enabled{id="9",name="Twitch"}`, "1"},
		{"a reconnecting destination is not up", `polyemesis_destination_up{id="9",name="Twitch"}`, "0"},
		{"destination kbps is exposed as bits per second", `polyemesis_destination_bitrate_bits_per_second{id="9",name="Twitch"}`, "4.5e+06"},
		{"destination restarts are counted", `polyemesis_destination_restarts_total{id="9",name="Twitch"}`, "7"},
		{"dropped frames are counted", `polyemesis_destination_dropped_frames_total{id="9",name="Twitch"}`, "12"},
		{"a disabled destination still reports", `polyemesis_destination_enabled{id="3",name="Archive"}`, "0"},

		{"relay subscribers", "polyemesis_relay_subscribers", "4"},
		{"relay rx packets", "polyemesis_relay_received_packets_total", "100"},
		{"relay rx bytes", "polyemesis_relay_received_bytes_total", "131600"},
		{"relay tx packets", "polyemesis_relay_transmitted_packets_total", "400"},
		{"relay drops", "polyemesis_relay_dropped_packets_total", "5"},

		{"recording file count", "polyemesis_recording_files", "6"},
		{"recording bytes used", "polyemesis_recording_used_bytes", "1.073741824e+09"},
		{"recording bytes free", "polyemesis_recording_free_bytes", "1.099511627776e+12"},

		{"process cpu", "polyemesis_process_cpu_percent", "12.5"},
		{"process rss", "polyemesis_process_resident_memory_bytes", "512"},
		{"host cpu", "polyemesis_host_cpu_percent", "41.5"},
		{"host cpu count", "polyemesis_host_cpus", "8"},
		{"host memory total", "polyemesis_host_memory_total_bytes", "4096"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := got[tt.series]
			if !ok {
				t.Fatalf("series %s is missing from the exposition", tt.series)
			}
			if v != tt.want {
				t.Errorf("%s = %s, want %s", tt.series, v, tt.want)
			}
		})
	}
}

func TestExactlyOneStateSampleIsSetPerProcess(t *testing.T) {
	got := parse(t, Render(testSnapshot()))

	tests := []struct {
		name    string
		prefix  string
		current string
	}{
		{"ingest reports running", "polyemesis_ingest_state{", "running"},
		{"destination reports reconnecting", `polyemesis_destination_state{id="9",name="Twitch",`, "reconnecting"},
		{"stopped destination reports stopped", `polyemesis_destination_state{id="3",name="Archive",`, "stopped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := 0
			for series, value := range got {
				if !strings.HasPrefix(series, tt.prefix) {
					continue
				}
				seen++
				want := "0"
				if strings.Contains(series, `state="`+tt.current+`"`) {
					want = "1"
				}
				if value != want {
					t.Errorf("%s = %s, want %s", series, value, want)
				}
			}
			if seen != len(processStates) {
				t.Errorf("got %d state samples, want one per state (%d)", seen, len(processStates))
			}
		})
	}
}

// A parser rejects the whole scrape if a family's samples are split by another
// family, or if its header is missing, so both are pinned here.
func TestEveryFamilyIsDeclaredOnceAndItsSamplesAreContiguous(t *testing.T) {
	text := Render(testSnapshot())

	declared := map[string]bool{}
	typed := map[string]bool{}
	closed := map[string]bool{}
	current := ""

	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name, help, ok := strings.Cut(strings.TrimPrefix(line, "# HELP "), " ")
			if !ok || help == "" {
				t.Errorf("HELP without text: %q", line)
			}
			if declared[name] {
				t.Errorf("family %s declared twice", name)
			}
			declared[name] = true
		case strings.HasPrefix(line, "# TYPE "):
			name, typ, _ := strings.Cut(strings.TrimPrefix(line, "# TYPE "), " ")
			if !declared[name] {
				t.Errorf("TYPE for %s precedes its HELP", name)
			}
			if typ != "gauge" && typ != "counter" {
				t.Errorf("family %s has type %q", name, typ)
			}
			if typ == "counter" && !strings.HasSuffix(name, "_total") {
				t.Errorf("counter %s does not end in _total", name)
			}
			if typ == "gauge" && strings.HasSuffix(name, "_total") {
				t.Errorf("gauge %s ends in _total", name)
			}
			typed[name] = true
		default:
			series, _, _ := strings.Cut(line, " ")
			name, _, _ := strings.Cut(series, "{")
			if !typed[name] {
				t.Fatalf("sample %q has no HELP/TYPE header", line)
			}
			if name != current {
				if closed[name] {
					t.Errorf("samples of %s are not contiguous", name)
				}
				if current != "" {
					closed[current] = true
				}
				current = name
			}
		}
	}

	for name := range declared {
		if !typed[name] {
			t.Errorf("family %s has HELP but no TYPE", name)
		}
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("exposition does not end with a newline")
	}
}

// processStates is a hand-copied list, and a state it does not know about
// would silently report every state series as 0. Test-only import: the package
// itself stays a pure formatter.
func TestEverySupervisorStateHasASeries(t *testing.T) {
	tests := []supervisor.State{
		supervisor.StateStopped,
		supervisor.StateStarting,
		supervisor.StateRunning,
		supervisor.StateReconnecting,
		supervisor.StateFailed,
	}

	for _, want := range tests {
		t.Run(string(want), func(t *testing.T) {
			for _, got := range processStates {
				if got == string(want) {
					return
				}
			}
			t.Errorf("supervisor state %q is missing from processStates %v", want, processStates)
		})
	}
	if len(tests) != len(processStates) {
		t.Errorf("processStates has %d entries, supervisor has %d", len(processStates), len(tests))
	}
}

func TestLabelValuesSurviveCharactersThatWouldBreakTheFormat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text is untouched", "Twitch Main", "Twitch Main"},
		{"quotes are escaped", `say "hi"`, `say \"hi\"`},
		{"backslashes are escaped", `C:\out`, `C:\\out`},
		{"newlines are escaped", "two\nlines", `two\nlines`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeLabel(tt.in); got != tt.want {
				t.Errorf("escapeLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDestinationSeriesAreOrderedByIDSoScrapesAreStable(t *testing.T) {
	s := testSnapshot()
	first := Render(s)

	s.Destinations[0], s.Destinations[1] = s.Destinations[1], s.Destinations[0]
	if second := Render(s); second != first {
		t.Error("reordering the destination slice changed the exposition")
	}

	archive := strings.Index(first, `polyemesis_destination_info{id="3"`)
	twitch := strings.Index(first, `polyemesis_destination_info{id="9"`)
	if archive < 0 || twitch < 0 || archive > twitch {
		t.Errorf("destinations are not ordered by id (id=3 at %d, id=9 at %d)", archive, twitch)
	}
}

func TestNonFiniteValuesRenderAsPrometheusLiterals(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want string
	}{
		{"whole numbers lose the decimal point", 90, "90"},
		{"large values use exponent notation", 6_000_000, "6e+06"},
		{"not a number", math.NaN(), "NaN"},
		{"positive infinity", math.Inf(1), "+Inf"},
		{"negative infinity", math.Inf(-1), "-Inf"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatValue(tt.in); got != tt.want {
				t.Errorf("formatValue(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
