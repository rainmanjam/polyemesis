package api

import (
	"net/http"
	"strings"
	"testing"
)

// The scrape carries the alert delivery counters on an install with a
// manager, from the same sums GET /alerts/meta serves. A webhook that has
// stopped taking deliveries can then page through Prometheus, which is the one
// path that does not depend on that webhook.
//
// Mutation: drop the snap.Alerts assignment from handleMetrics. Observed to
// fail with "no polyemesis_alert_deliveries_total".
func TestTheScrapeCarriesAlertDeliveries(t *testing.T) {
	_, h, _, auth := managerServer(t, defaultTools())
	body := scrape(t, h, auth)
	for _, want := range []string{
		`polyemesis_alert_deliveries_total{result="sent"} 0`,
		`polyemesis_alert_deliveries_total{result="failed"} 0`,
		"# TYPE polyemesis_alert_last_success_timestamp_seconds gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no polyemesis_alert_deliveries_total: missing %q in the scrape", want)
		}
	}
}

func scrape(t *testing.T, h http.Handler, auth func(*http.Request)) string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}
