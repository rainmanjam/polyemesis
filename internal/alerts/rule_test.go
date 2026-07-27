package alerts

import (
	"strings"
	"testing"
	"time"
)

func TestRuleValidateRefusesWhatCannotBeDelivered(t *testing.T) {
	ok := Rule{Name: "ops", URL: "https://example.test/hook", Format: FormatJSON, MinSeverity: SeverityInfo}
	tests := []struct {
		name    string
		mut     func(*Rule)
		wantErr string
	}{
		{name: "a complete rule", mut: func(*Rule) {}},
		{name: "no name", mut: func(r *Rule) { r.Name = "" }, wantErr: "needs a name"},
		{name: "no URL", mut: func(r *Rule) { r.URL = "" }, wantErr: "needs a webhook URL"},
		{
			name:    "a scheme that cannot be posted to",
			mut:     func(r *Rule) { r.URL = "rtmp://example.test/live" },
			wantErr: "must post to http or https",
		},
		{
			name:    "no host",
			mut:     func(r *Rule) { r.URL = "https:///hook" },
			wantErr: "no host",
		},
		{
			name:    "an unknown format",
			mut:     func(r *Rule) { r.Format = "xmpp" },
			wantErr: "unknown format",
		},
		{
			name:    "an unknown severity",
			mut:     func(r *Rule) { r.MinSeverity = "loud" },
			wantErr: "unknown severity",
		},
		{
			name:    "a subscription to an event that does not exist",
			mut:     func(r *Rule) { r.Events = []Type{"destination.exploded"} },
			wantErr: "unknown event",
		},
		{
			name:    "a name longer than the column",
			mut:     func(r *Rule) { r.Name = strings.Repeat("x", MaxRuleNameLen+1) },
			wantErr: "longer than",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ok
			tt.mut(&r)
			err := r.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateNeverQuotesTheWebhookPathBackAtTheUser(t *testing.T) {
	r := Rule{Name: "ops", URL: "https://hooks.slack.com/services/T/B/SECRETPATH", Format: "xmpp"}
	err := r.Validate()
	if err == nil {
		t.Fatal("Validate accepted an unknown format")
	}
	if strings.Contains(err.Error(), "SECRETPATH") {
		t.Errorf("the error message leaked the webhook path: %v", err)
	}
}

func TestRuleNormalizedClampsRatherThanRefuses(t *testing.T) {
	tests := []struct {
		name             string
		in               Rule
		wantDebounce     int
		wantMinInterval  int
		wantFormat       Format
		wantMinSeverity  Severity
		wantEventsLength int
	}{
		{
			name:            "an empty rule takes the defaults",
			in:              Rule{Name: " ops "},
			wantDebounce:    DefaultDebounceSeconds,
			wantMinInterval: DefaultIntervalSeconds,
			wantFormat:      FormatJSON,
			wantMinSeverity: SeverityInfo,
		},
		{
			name:            "a debounce past the ceiling is clamped, not rejected",
			in:              Rule{Name: "ops", DebounceSeconds: 1 << 20, MinIntervalSeconds: 1 << 20},
			wantDebounce:    MaxDebounceSeconds,
			wantMinInterval: MaxIntervalSeconds,
			wantFormat:      FormatJSON,
			wantMinSeverity: SeverityInfo,
		},
		{
			name: "duplicate and unknown subscriptions are dropped",
			in: Rule{Name: "ops", Events: []Type{
				TypeDiskLow, TypeDiskLow, "destination.exploded", TypeClipping,
			}},
			wantDebounce:     DefaultDebounceSeconds,
			wantMinInterval:  DefaultIntervalSeconds,
			wantFormat:       FormatJSON,
			wantMinSeverity:  SeverityInfo,
			wantEventsLength: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in.Normalized()
			if got.DebounceSeconds != tt.wantDebounce {
				t.Errorf("DebounceSeconds = %d, want %d", got.DebounceSeconds, tt.wantDebounce)
			}
			if got.MinIntervalSeconds != tt.wantMinInterval {
				t.Errorf("MinIntervalSeconds = %d, want %d", got.MinIntervalSeconds, tt.wantMinInterval)
			}
			if got.Format != tt.wantFormat {
				t.Errorf("Format = %q, want %q", got.Format, tt.wantFormat)
			}
			if got.MinSeverity != tt.wantMinSeverity {
				t.Errorf("MinSeverity = %q, want %q", got.MinSeverity, tt.wantMinSeverity)
			}
			if len(got.Events) != tt.wantEventsLength {
				t.Errorf("Events = %v, want %d of them", got.Events, tt.wantEventsLength)
			}
			if strings.TrimSpace(got.Name) != got.Name {
				t.Errorf("Name = %q, want it trimmed", got.Name)
			}
		})
	}
}

func TestRuleWindowsDefaultWhenZero(t *testing.T) {
	var r Rule
	if r.Debounce() != DefaultDebounceSeconds*time.Second {
		t.Errorf("Debounce = %v, want the default", r.Debounce())
	}
	if r.MinInterval() != DefaultIntervalSeconds*time.Second {
		t.Errorf("MinInterval = %v, want the default", r.MinInterval())
	}
}

func TestSeverityAtLeast(t *testing.T) {
	tests := []struct {
		sev, floor Severity
		want       bool
	}{
		{sev: SeverityCritical, floor: SeverityWarning, want: true},
		{sev: SeverityWarning, floor: SeverityWarning, want: true},
		{sev: SeverityInfo, floor: SeverityWarning, want: false},
		{sev: SeverityInfo, floor: SeverityInfo, want: true},
		// An unclassified severity is still an alert and passes an info floor.
		{sev: "", floor: SeverityInfo, want: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.sev)+"/"+string(tt.floor), func(t *testing.T) {
			if got := tt.sev.AtLeast(tt.floor); got != tt.want {
				t.Errorf("%q.AtLeast(%q) = %v, want %v", tt.sev, tt.floor, got, tt.want)
			}
		})
	}
}

func TestAllTypesAreKnownAndTestIsNotSubscribable(t *testing.T) {
	for _, ty := range AllTypes() {
		if !KnownType(ty) {
			t.Errorf("AllTypes lists %q but KnownType refuses it", ty)
		}
	}
	if KnownType(TypeTest) {
		t.Error("TypeTest must not be subscribable; it always delivers")
	}
}

func TestWithFieldSkipsEmptyValuesAndDoesNotShareBackingArrays(t *testing.T) {
	a := Event{}.WithField("one", "1")
	b := a.WithField("two", "2")
	c := a.WithField("three", "3")

	if len(a.Fields) != 1 {
		t.Fatalf("the original event grew to %d fields", len(a.Fields))
	}
	if b.Fields[1].Name != "two" || c.Fields[1].Name != "three" {
		t.Errorf("two branches share a backing array: %v vs %v", b.Fields, c.Fields)
	}
	if got := a.WithField("empty", ""); len(got.Fields) != 1 {
		t.Errorf("an empty value added a field: %v", got.Fields)
	}
}
