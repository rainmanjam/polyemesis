package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/rainmanjam/polyemesis/internal/failreason"
)

// The shipped Grafana dashboard and Prometheus rules are checked against what
// this package actually emits (#13). A rename here that the dashboard did not
// follow is a panel that silently reads "No data" on every install, and an
// alert rule over a series that no longer exists is an alert that can never
// fire -- neither fails anywhere else.

var (
	dashboardPath = filepath.Join("..", "..", "deploy", "grafana", "polyemesis.json")
	alertsPath    = filepath.Join("..", "..", "deploy", "prometheus", "alerts.yml")
	metricNameRE  = regexp.MustCompile(`\bpolyemesis_[a-zA-Z0-9_]*\b`)
)

type dashPanel struct {
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Datasource json.RawMessage `json:"datasource"`
	Targets    []struct {
		Expr       string          `json:"expr"`
		Datasource json.RawMessage `json:"datasource"`
	} `json:"targets"`
	// A collapsed row nests its panels here.
	Panels []dashPanel `json:"panels"`
}

type dashboard struct {
	UID        string      `json:"uid"`
	Panels     []dashPanel `json:"panels"`
	Templating struct {
		List []struct {
			Name       string          `json:"name"`
			Type       string          `json:"type"`
			Query      json.RawMessage `json:"query"`
			Definition string          `json:"definition"`
			AllValue   string          `json:"allValue"`
		} `json:"list"`
	} `json:"templating"`
}

func loadDashboard(t *testing.T) dashboard {
	t.Helper()
	raw, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatalf("reading %s: %v", dashboardPath, err)
	}
	var d dashboard
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("%s does not parse: %v", dashboardPath, err)
	}
	return d
}

func walkPanels(ps []dashPanel, fn func(dashPanel)) {
	for _, p := range ps {
		fn(p)
		walkPanels(p.Panels, fn)
	}
}

// dashboardQueries is every PromQL string in the dashboard, keyed by where it
// came from so a failure names the panel.
func dashboardQueries(t *testing.T, d dashboard) map[string]string {
	t.Helper()
	out := map[string]string{}
	walkPanels(d.Panels, func(p dashPanel) {
		for i, tg := range p.Targets {
			if strings.TrimSpace(tg.Expr) == "" {
				t.Errorf("panel %q target %d has no expr", p.Title, i)
				continue
			}
			out["panel "+p.Title+" #"+string(rune('A'+i))] = tg.Expr
		}
	})
	for _, v := range d.Templating.List {
		if v.Type != "query" {
			continue
		}
		q := v.Definition
		var s string
		var obj struct {
			Query string `json:"query"`
		}
		switch {
		case json.Unmarshal(v.Query, &s) == nil:
			q += " " + s
		case json.Unmarshal(v.Query, &obj) == nil:
			q += " " + obj.Query
		}
		out["variable "+v.Name] = q
	}
	return out
}

type ruleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert       string            `yaml:"alert"`
			Record      string            `yaml:"record"`
			Expr        string            `yaml:"expr"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

func loadRules(t *testing.T) ruleFile {
	t.Helper()
	raw, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", alertsPath, err)
	}
	var rf ruleFile
	if err := yaml.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("%s does not parse: %v", alertsPath, err)
	}
	return rf
}

func checkNames(t *testing.T, file string, queries map[string]string, registered map[string]bool) {
	t.Helper()
	for where, q := range queries {
		names := metricNameRE.FindAllString(q, -1)
		if len(names) == 0 && !strings.HasPrefix(where, "annotation") {
			t.Errorf("%s: %s queries no polyemesis_* metric: %q", file, where, q)
		}
		for _, n := range names {
			if !registered[n] {
				t.Errorf("%s: %s references %q, which internal/metrics does not emit", file, where, n)
			}
		}
	}
}

// TestDashboardQueriesOnlyMetricsThatExist is the CI drift test the plan asks
// for: every metric any panel or variable queries is one Render emits.
func TestDashboardQueriesOnlyMetricsThatExist(t *testing.T) {
	d := loadDashboard(t)
	qs := dashboardQueries(t, d)
	if len(qs) < 20 {
		t.Fatalf("found only %d queries in the dashboard -- did the panel layout change shape?", len(qs))
	}
	checkNames(t, "polyemesis.json", qs, registeredMetricNames(t))
}

// TestAlertRulesQueryOnlyMetricsThatExist: the same for the Prometheus rules,
// including the metrics looked up from inside annotation templates.
func TestAlertRulesQueryOnlyMetricsThatExist(t *testing.T) {
	rf := loadRules(t)
	qs := map[string]string{}
	alerts := map[string]bool{}
	for _, g := range rf.Groups {
		for _, r := range g.Rules {
			name := r.Alert + r.Record
			qs["rule "+name] = r.Expr
			alerts[r.Alert] = true
			for k, v := range r.Annotations {
				qs["annotation "+name+"."+k] = v
			}
		}
	}
	// The four the plan agreed: destination down, ingest down, disk floor,
	// restart storm.
	for _, want := range []string{"PolyemesisDestinationDown", "PolyemesisIngestDown",
		"PolyemesisRecordingDiskLow", "PolyemesisDestinationRestartStorm", "PolyemesisIngestRestartStorm"} {
		if !alerts[want] {
			t.Errorf("alerts.yml has no %s rule", want)
		}
	}
	checkNames(t, "alerts.yml", qs, registeredMetricNames(t))
}

// TestDashboardIsPortableAndFiltersByProgramme pins the choices that make the
// one file work both provisioned from disk and imported from grafana.com, and
// the one join that is easy to get wrong.
func TestDashboardIsPortableAndFiltersByProgramme(t *testing.T) {
	d := loadDashboard(t)
	if d.UID == "" {
		t.Error("the dashboard has no uid; a provisioned copy and an imported one would not be the same dashboard")
	}
	vars := map[string]string{}
	for _, v := range d.Templating.List {
		vars[v.Name] = v.Type
		if v.Name == "programme" && v.AllValue != ".*" {
			// source_id is "" for a destination in no programme; All must
			// still match it.
			t.Errorf("programme allValue = %q, want .*", v.AllValue)
		}
	}
	if vars["datasource"] != "datasource" || vars["programme"] != "query" {
		t.Fatalf("want a datasource variable and a programme query variable, got %v", vars)
	}

	walkPanels(d.Panels, func(p dashPanel) {
		if p.Type == "row" || p.Type == "text" {
			return
		}
		for _, raw := range append([]json.RawMessage{p.Datasource}, func() (r []json.RawMessage) {
			for _, tg := range p.Targets {
				r = append(r, tg.Datasource)
			}
			return
		}()...) {
			// Never a hard-coded uid, and never ${DS_...} from __inputs, which
			// file provisioning does not resolve.
			if !strings.Contains(string(raw), `"${datasource}"`) {
				t.Errorf("panel %q datasource is %s, want uid ${datasource}", p.Title, raw)
			}
		}
		for _, tg := range p.Targets {
			// Destination series carry the DESTINATION's id. Filtering one by
			// the programme's id matches some other destination, silently.
			if strings.Contains(tg.Expr, "polyemesis_destination_") && strings.Contains(tg.Expr, `id=~"$programme"`) &&
				!strings.Contains(tg.Expr, `source_id=~"$programme"`) {
				t.Errorf("panel %q filters a destination series by the programme id: %s", p.Title, tg.Expr)
			}
		}
	})
}

// TestDestinationHealthOnTheDashboardIncludesTheCause: the destination row and
// the rules carry the #20 failure cause, and every cause value either one
// spells out is a published code.
func TestDestinationHealthOnTheDashboardIncludesTheCause(t *testing.T) {
	raw, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := os.ReadFile(alertsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range []string{"polyemesis_destination_failures_total", "polyemesis_destination_failure_reason", "polyemesis_destination_held"} {
		if !strings.Contains(string(raw), fam) {
			t.Errorf("the dashboard does not show %s", fam)
		}
	}
	if !strings.Contains(string(rules), "polyemesis_destination_failure_reason") {
		t.Error("the destination alert does not say why")
	}
	known := map[string]bool{}
	for _, c := range failreason.AllCauses() {
		known[string(c)] = true
	}
	// Value mappings in the dashboard key on cause codes; an unknown key is a
	// mapping that never applies.
	var d struct {
		Panels []struct {
			FieldConfig struct {
				Defaults struct {
					Mappings []struct {
						Options map[string]json.RawMessage `json:"options"`
					} `json:"mappings"`
				} `json:"defaults"`
			} `json:"fieldConfig"`
			Title string `json:"title"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	mapped := 0
	for _, p := range d.Panels {
		if !strings.Contains(p.Title, "failing") {
			continue
		}
		for _, m := range p.FieldConfig.Defaults.Mappings {
			for k := range m.Options {
				mapped++
				if !known[k] {
					t.Errorf("panel %q maps cause %q, which is not a published cause", p.Title, k)
				}
			}
		}
	}
	if mapped != len(known) {
		t.Errorf("the failing-now panel maps %d causes, want all %d", mapped, len(known))
	}
	for _, src := range [][]byte{raw, rules} {
		for _, m := range regexp.MustCompile(`cause\s*=~?\s*\\?"([^"\\]*)`).FindAllSubmatch(src, -1) {
			if !known[string(m[1])] {
				t.Errorf("a query matches cause=%q, which is not a published cause", m[1])
			}
		}
	}
}
