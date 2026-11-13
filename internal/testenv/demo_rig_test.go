package testenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/demorig"
)

// The sports scene of the demo rig: one match feed, five destinations that are
// labelled YouTube but publish only to the local receiver.

type sportsFixture struct {
	Scene   string `json:"scene"`
	Sources []struct {
		Name  string                     `json:"name"`
		Video map[string]json.RawMessage `json:"video"`
		Audio []struct {
			Label string `json:"label"`
		} `json:"audio"`
		Destinations []struct {
			Name      string `json:"name"`
			Kind      string `json:"kind"`
			Platform  string `json:"platform"`
			URL       string `json:"url"`
			BackupURL string `json:"backupUrl"`
			StreamKey string `json:"streamKey"`
			Enabled   bool   `json:"enabled"`
		} `json:"destinations"`
	} `json:"sources"`
}

const sportsReceiverHost = "polyemesis-demo-recv"

func loadSportsFixture(t *testing.T) (sportsFixture, string) {
	t.Helper()
	root := repoRootFromTest(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "demo-seed.sports.fixture.json"))
	if err != nil {
		t.Fatalf("read sports fixture: %v", err)
	}
	var fx sportsFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse sports fixture: %v", err)
	}
	return fx, root
}

func TestSportsFixtureIsOneProgrammeFiveLocalDestinations(t *testing.T) {
	fx, _ := loadSportsFixture(t)
	if fx.Scene != "sports" {
		t.Errorf("scene = %q, want sports", fx.Scene)
	}
	if len(fx.Sources) != 1 {
		t.Fatalf("want one source, got %d", len(fx.Sources))
	}
	src := fx.Sources[0]
	if _, has := src.Video["lavfi"]; has {
		t.Error("video must not carry lavfi: the media is scripts/demo-sports/")
	}
	var labels []string
	for _, a := range src.Audio {
		labels = append(labels, a.Label)
	}
	wantLabels := []string{"English", "Español", "Português", "Français", "Arabic", "Crowd"}
	if strings.Join(labels, "|") != strings.Join(wantLabels, "|") {
		t.Errorf("audio labels = %v, want %v", labels, wantLabels)
	}
	wantNames := []string{"YouTube · English", "YouTube · Español", "YouTube · Português", "YouTube · Français", "YouTube · Arabic"}
	if len(src.Destinations) != len(wantNames) {
		t.Fatalf("want %d destinations, got %d", len(wantNames), len(src.Destinations))
	}
	var rows []demorig.DestRow
	for i, d := range src.Destinations {
		if d.Name != wantNames[i] {
			t.Errorf("destination %d = %q, want %q", i, d.Name, wantNames[i])
		}
		if d.Enabled {
			t.Errorf("%s must start disabled", d.Name)
		}
		if d.Kind != "rtmp" || d.Platform != "youtube" {
			t.Errorf("%s: kind/platform = %s/%s, want rtmp/youtube", d.Name, d.Kind, d.Platform)
		}
		rows = append(rows, demorig.DestRow{Name: d.Name, URL: d.URL, BackupURL: d.BackupURL})
	}
	if err := demorig.CheckHosts(rows, []string{sportsReceiverHost}); err != nil {
		t.Errorf("fixture destinations are not all local: %v", err)
	}
}

func TestSportsFixtureCarriesNoRealKey(t *testing.T) {
	fx, _ := loadSportsFixture(t)
	re := regexp.MustCompile(`^demo-local-[a-z]{2}-not-a-real-key$`)
	n := 0
	for _, d := range fx.Sources[0].Destinations {
		n++
		if !re.MatchString(d.StreamKey) {
			t.Errorf("%s: stream key does not look invented (length %d)", d.Name, len(d.StreamKey))
		}
	}
	if n == 0 {
		t.Error("no destinations checked")
	}
}

func TestSportsReceiverNameAgrees(t *testing.T) {
	// Stays red until scripts/lib-demo-sports.sh exists (plan 3, Task 4).
	root := repoRootFromTest(t)
	body, err := os.ReadFile(filepath.Join(root, "scripts", "lib-demo-sports.sh"))
	if err != nil {
		t.Fatalf("read lib-demo-sports.sh: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*(?:readonly\s+)?RECV="?\$\{RECV:-([^}"]+)\}"?`).FindSubmatch(body)
	if m == nil {
		t.Fatal("no RECV=\"${RECV:-<name>}\" default in scripts/lib-demo-sports.sh")
	}
	if string(m[1]) != sportsReceiverHost {
		t.Errorf("script receiver %q != fixture host %q", m[1], sportsReceiverHost)
	}
}

func TestSportsMediaHasNoSweepingVibrato(t *testing.T) {
	root := repoRootFromTest(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "demo-sports", "match.filter"))
	if err != nil {
		t.Fatalf("read match.filter: %v", err)
	}
	body := string(raw)
	// A vibrato written (F+6*sin(..))*t makes the pitch sweep further from F every
	// second, so the proof would pass for a minute and fail on a long run.
	for _, hz := range []string{"118", "152", "186", "224", "98"} {
		if !strings.Contains(body, "sin(2*PI*"+hz+"*t+12*sin(2*PI*0.3*t))") {
			t.Errorf("voice %s Hz lacks the phase-modulated form", hz)
		}
		if strings.Contains(body, "("+hz+"+6*sin(") {
			t.Errorf("voice %s Hz uses a sweeping vibrato", hz)
		}
	}
}
