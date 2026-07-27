package alerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleDelivery(format Format, mut ...func(*Delivery)) Delivery {
	d := Delivery{
		Rule: testRule(func(r *Rule) { r.Format = format }),
		Items: []Item{{
			Event: Event{
				Type: TypeDestinationDown, Severity: SeverityCritical, Key: "destination:2",
				Title: "Destination down: Twitch",
				Text:  "Twitch has not been delivering for 40s.",
				Fields: []Field{
					{Name: "destination", Value: "Twitch"},
					{Name: "platform", Value: "twitch"},
				},
				At: base,
			},
			Count: 3, First: base, Last: base.Add(30 * time.Second),
		}},
	}
	for _, m := range mut {
		m(&d)
	}
	return d
}

func TestEncodeProducesTheShapeEachServiceExpects(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		body, ct, err := Encode(sampleDelivery(FormatJSON))
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if ct != "application/json" {
			t.Errorf("content type = %q", ct)
		}
		var p struct {
			Source string `json:"source"`
			Rule   string `json:"rule"`
			Alerts []struct {
				Type   Type              `json:"type"`
				Count  int               `json:"count"`
				Fields map[string]string `json:"fields"`
			} `json:"alerts"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, body)
		}
		if p.Source != "polyemesis" || p.Rule != "ops" {
			t.Errorf("source/rule = %q/%q", p.Source, p.Rule)
		}
		if len(p.Alerts) != 1 {
			t.Fatalf("alerts = %d, want 1", len(p.Alerts))
		}
		if p.Alerts[0].Type != TypeDestinationDown || p.Alerts[0].Count != 3 {
			t.Errorf("alert = %+v", p.Alerts[0])
		}
		if p.Alerts[0].Fields["destination"] != "Twitch" {
			t.Errorf("fields = %v, want them keyed by name for a script to index", p.Alerts[0].Fields)
		}
	})

	t.Run("discord", func(t *testing.T) {
		body, _, err := Encode(sampleDelivery(FormatDiscord))
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		var p discordPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, body)
		}
		if p.Username != "polyemesis" {
			t.Errorf("username = %q", p.Username)
		}
		if len(p.Embeds) != 1 {
			t.Fatalf("embeds = %d, want 1", len(p.Embeds))
		}
		e := p.Embeds[0]
		if !strings.Contains(e.Title, "(x3)") {
			t.Errorf("title = %q, want the occurrence count in it", e.Title)
		}
		if e.Color != colorCritical {
			t.Errorf("color = %#x, want the critical colour %#x", e.Color, colorCritical)
		}
		if len(e.Fields) != 2 {
			t.Errorf("fields = %d, want 2", len(e.Fields))
		}
		if _, err := time.Parse(time.RFC3339, e.Timestamp); err != nil {
			t.Errorf("timestamp %q is not RFC3339, which Discord rejects", e.Timestamp)
		}
	})

	t.Run("slack", func(t *testing.T) {
		body, _, err := Encode(sampleDelivery(FormatSlack))
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		var p slackPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, body)
		}
		if !strings.HasPrefix(p.Text, "polyemesis: ") {
			t.Errorf("text = %q, want a usable phone notification line", p.Text)
		}
		if len(p.Attachments) != 1 {
			t.Fatalf("attachments = %d, want 1", len(p.Attachments))
		}
		if p.Attachments[0].Color != "#ED4245" {
			t.Errorf("color = %q, want the critical colour", p.Attachments[0].Color)
		}
		if p.Attachments[0].TS != base.Add(30*time.Second).Unix() {
			t.Errorf("ts = %d, want the newest occurrence", p.Attachments[0].TS)
		}
	})

	t.Run("unknown format is refused rather than silently posted as json", func(t *testing.T) {
		d := sampleDelivery(FormatJSON)
		d.Rule.Format = "xmpp"
		if _, _, err := Encode(d); err == nil {
			t.Fatal("Encode accepted an unknown format")
		}
	})
}

func TestEncodeSaysHowManyAlertsDidNotFit(t *testing.T) {
	d := sampleDelivery(FormatDiscord, func(d *Delivery) { d.Overflow = 4 })
	body, _, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var p discordPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(p.Content, "4 more") {
		t.Errorf("content = %q, want it to admit the 4 dropped alerts", p.Content)
	}
}

func TestEncodeStaysInsideTheServicesLengthLimits(t *testing.T) {
	long := strings.Repeat("x", 9000)
	d := sampleDelivery(FormatDiscord, func(d *Delivery) {
		d.Items[0].Event.Title = long
		d.Items[0].Event.Text = long
		d.Items[0].Event.Fields = []Field{{Name: long, Value: long}}
	})
	body, _, err := Encode(d)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var p discordPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := p.Embeds[0]
	if len(e.Title) > discordMaxTitle {
		t.Errorf("title is %d bytes, over Discord's %d", len(e.Title), discordMaxTitle)
	}
	if len(e.Description) > discordMaxDesc {
		t.Errorf("description is %d bytes, over Discord's %d", len(e.Description), discordMaxDesc)
	}
	if len(e.Fields[0].Value) > discordMaxFieldValue {
		t.Errorf("field value is %d bytes, over Discord's %d", len(e.Fields[0].Value), discordMaxFieldValue)
	}
}

func TestTruncCutsOnARuneBoundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "short enough is untouched", in: "hello", max: 10, want: "hello"},
		{name: "exactly at the limit is untouched", in: "hello", max: 5, want: "hello"},
		{name: "ascii is cut and marked", in: "abcdefghij", max: 6, want: "abc..."},
		{name: "multi-byte runes are not split", in: "ααααα", max: 7, want: "αα..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trunc(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("trunc(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if len(got) > tt.max {
				t.Errorf("trunc(%q, %d) = %q, which is %d bytes", tt.in, tt.max, got, len(got))
			}
		})
	}
}

func TestSlackSummaryCountsEveryAlertIncludingTheOverflow(t *testing.T) {
	d := sampleDelivery(FormatSlack, func(d *Delivery) {
		d.Items = append(d.Items, d.Items[0])
		d.Overflow = 3
	})
	if got := slackSummary(d); !strings.Contains(got, "5 alerts") {
		t.Errorf("slackSummary = %q, want it to count the 2 shown plus the 3 that were not", got)
	}
}
