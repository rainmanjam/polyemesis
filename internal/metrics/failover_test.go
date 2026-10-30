package metrics

import (
	"strings"
	"testing"
)

// Degraded-link failover (#6) on the scrape. Each programme gets a degraded
// gauge that sits at 0 rather than being absent -- an alert on a series that
// does not exist yet never fires -- and its on-air source as one series per
// kind. The SRT link figures are different: an RTMP primary has no RTT, and a
// series reading 0 would claim a perfect link, so those are absent until
// measured.
func TestFailoverStateAndLinkHealthAreOnTheScrape(t *testing.T) {
	rtt, loss := 240.0, 3.5
	out := Render(Snapshot{Ingests: []Ingest{
		{Process: Process{State: "running"}, ID: 1, Name: "IRL",
			Failover: &Failover{Active: "slate", Degraded: true, RTTMs: &rtt, LossPercent: &loss}},
		{Process: Process{State: "running"}, ID: 2, Name: "Studio"},
	}})
	for _, want := range []string{
		`polyemesis_failover_degraded{id="1",name="IRL"} 1`,
		`polyemesis_failover_degraded{id="2",name="Studio"} 0`,
		`polyemesis_failover_active{id="1",name="IRL",source="slate"} 1`,
		`polyemesis_failover_active{id="1",name="IRL",source="primary"} 0`,
		`polyemesis_ingest_link_rtt_seconds{id="1",name="IRL"} 0.24`,
		`polyemesis_ingest_link_loss_ratio{id="1",name="IRL"} 0.035`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, absent := range []string{
		`polyemesis_failover_active{id="2"`,
		`polyemesis_ingest_link_rtt_seconds{id="2"`,
		`polyemesis_ingest_link_loss_ratio{id="2"`,
	} {
		if strings.Contains(out, absent) {
			t.Errorf("a programme with no failover tier and no SRT link has a %s series", absent)
		}
	}
	empty := Render(Snapshot{})
	for _, fam := range []string{"polyemesis_failover_degraded", "polyemesis_failover_active",
		"polyemesis_ingest_link_rtt_seconds", "polyemesis_ingest_link_loss_ratio"} {
		if !strings.Contains(empty, "# TYPE "+fam+" gauge") {
			t.Errorf("the %s family header is missing on an empty install", fam)
		}
	}
}
