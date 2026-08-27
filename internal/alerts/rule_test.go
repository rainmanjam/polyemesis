package alerts

import (
	"errors"
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
			wantErr: "destination.exploded",
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
			// Three kept, not two: the duplicate goes and the unknown name
			// STAYS, so Validate can refuse it by name at save. Dropping it
			// here is what turned events:["nope.notreal"] into an empty list,
			// and an empty list means every event.
			name: "a duplicate subscription is dropped and an unknown one is kept",
			in: Rule{Name: "ops", Events: []Type{
				TypeDiskLow, TypeDiskLow, "destination.exploded", TypeClipping,
			}},
			wantDebounce:     DefaultDebounceSeconds,
			wantMinInterval:  DefaultIntervalSeconds,
			wantFormat:       FormatJSON,
			wantMinSeverity:  SeverityInfo,
			wantEventsLength: 3,
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

// POST /alerts/rules with events:["nope.notreal"] returned 201 and stored an
// empty list, and an empty list means every type. The narrowest rule an
// operator could write became the loudest thing on the install while they
// believed they had subscribed to one event. Normalized had deleted the name
// before Validate could look at it, so Validate's unknown-event branch was
// dead code and nothing anywhere said the word "notreal".
func TestATypoedEventNameIsRefusedByNameRatherThanWidenedToEverything(t *testing.T) {
	raw := Rule{Name: "disk", URL: "https://example.test/hook", Events: []Type{"nope.notreal"}}

	// Every write path in the tree is Normalized-then-Validate, so that is the
	// order driven here. Calling Validate on the raw value would prove nothing
	// about the path an operator actually reaches.
	norm := raw.Normalized()
	if len(norm.Events) == 0 {
		t.Fatal("Normalized emptied the subscription list; an empty list means " +
			"EVERY event, so the rule the operator wrote as one event is now all of them")
	}
	err := norm.Validate()
	if err == nil {
		t.Fatal("Validate accepted a subscription to an event that does not exist")
	}
	if !strings.Contains(err.Error(), "nope.notreal") {
		t.Errorf("error = %q, want it to name the event the operator mistyped", err)
	}

	// The control. A validator that refuses every event name would pass the
	// assertions above and quietly make the settings page unusable, so every
	// name the picker offers has to be accepted here.
	for _, ty := range AllTypes() {
		ok := Rule{Name: "disk", URL: "https://example.test/hook", Events: []Type{ty}}
		if err := ok.Normalized().Validate(); err != nil {
			t.Errorf("Validate refused %q, which AllTypes offers in the picker: %v", ty, err)
		}
	}
}

// The upgrade case, and the reason the refusal is at save and not at load. An
// install may already store a rule naming an event a later version removed.
// db.scanAlertRule runs Normalized and never Validate, so that rule must still
// load -- and it must stay NARROW while it does. Under the old code Normalized
// deleted the stale name on read, the list went empty, and a rule nobody had
// touched started firing on everything.
func TestARuleStoredAgainstARetiredEventStillLoadsAndStaysNarrow(t *testing.T) {
	stored := Rule{
		Name: "disk", URL: "https://example.test/hook",
		Events: []Type{TypeDiskLow, "disk.retired.in.a.later.version"},
	}.Normalized()

	if len(stored.Events) != 2 {
		t.Fatalf("Events = %v, want the retired name kept so the list cannot go "+
			"empty and mean everything", stored.Events)
	}
	if !stored.Wants(Event{Type: TypeDiskLow, Severity: SeverityWarning}) {
		t.Error("the rule stopped alerting on the event it does name; a retired " +
			"name must cost that one subscription, not the whole rule")
	}
	if stored.Wants(Event{Type: TypeLoginFailed, Severity: SeverityWarning}) {
		t.Error("the rule now wants an event it never subscribed to; this is the " +
			"silent widening the retired name was supposed to cost nothing for")
	}
}

func TestCheckNameUniqueRefusesANameAlreadyInUse(t *testing.T) {
	existing := []Rule{
		{ID: 1, Name: "disk"},
		{ID: 2, Name: "Ingest"},
	}
	tests := []struct {
		name      string
		candidate Rule
		wantDup   bool
	}{
		// The controls. A checker that refuses every name would pass every
		// wantDup case below and lock the operator out of creating anything.
		{name: "a new name is free", candidate: Rule{Name: "loudness"}},
		{
			name:      "an existing rule keeping its own name is not its own duplicate",
			candidate: Rule{ID: 1, Name: "disk"},
		},
		{name: "an exact duplicate", candidate: Rule{Name: "disk"}, wantDup: true},
		{
			name: "a duplicate in different case, which the list cannot show apart",
			// Deliberately case-folded and space-padded: "Disk " and "disk"
			// render identically in a settings list, so treating them as
			// distinct hands the operator exactly the ambiguity the check exists
			// to remove.
			candidate: Rule{Name: " Disk "},
			wantDup:   true,
		},
		{
			name:      "renaming one rule onto another rule's name",
			candidate: Rule{ID: 2, Name: "disk"},
			wantDup:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckNameUnique(tt.candidate, existing)
			if !tt.wantDup {
				if err != nil {
					t.Fatalf("CheckNameUnique = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("CheckNameUnique accepted a name another rule already " +
					"answers to; the operator cannot tell the two apart in the list")
			}
			if !errors.Is(err, ErrDuplicateRuleName) {
				t.Errorf("error = %v, want it to wrap ErrDuplicateRuleName so the "+
					"HTTP layer can answer 409 rather than 400", err)
			}
			if !strings.Contains(err.Error(), "disk") {
				t.Errorf("error = %q, want it to name the rule already using it", err)
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
