package testenv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/demorig"
)

func jsonTagName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// demorig.DestRow decodes GET /destinations, so a server-side rename of one of
// the fields it decides on would decode as zero and pass the check. The tag
// names are pinned against db.Destination, ignoring omitempty.
func TestDemoRigDestRowTagsMatchTheServer(t *testing.T) {
	rowT := reflect.TypeOf(demorig.DestRow{})
	dbT := reflect.TypeOf(db.Destination{})
	for _, field := range []string{"URL", "BackupURL", "ExtraInputArgs", "ExtraOutputArgs", "AccountID", "Multitrack", "Kind"} {
		rf, ok := rowT.FieldByName(field)
		if !ok {
			t.Fatalf("demorig.DestRow has no field %s", field)
		}
		df, ok := dbT.FieldByName(field)
		if !ok {
			t.Fatalf("db.Destination has no field %s", field)
		}
		if got, want := jsonTagName(rf), jsonTagName(df); got == "" || got != want {
			t.Errorf("%s: demorig.DestRow decodes %q but db.Destination serves %q", field, got, want)
		}
		if rf.Type.Kind() != df.Type.Kind() && field != "Kind" {
			t.Errorf("%s: kinds differ (%s vs %s)", field, rf.Type.Kind(), df.Type.Kind())
		}
	}
}

// Every alert-rule and hook URL in scripts/seed_demo.go goes through the same
// check the full scene's fixture does: https on a .invalid host.
func TestSeedDemoWebhookURLsAreUnreachable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "scripts", "seed_demo.go"))
	if err != nil {
		t.Fatal(err)
	}
	var rules []demorig.WebhookRule
	for i, m := range regexp.MustCompile(`\burl:\s*"([^"]*)"`).FindAllStringSubmatch(string(raw), -1) {
		rules = append(rules, demorig.WebhookRule{Name: "seed_demo url #" + string(rune('0'+i%10)), URL: m[1]})
	}
	if len(rules) < 8 {
		t.Fatalf("found %d url: literals in seed_demo.go, want the five alert rules and three hooks", len(rules))
	}
	if err := demorig.CheckWebhookURLs(rules); err != nil {
		t.Errorf("%v", err)
	}
	if !strings.Contains(string(raw), "demorig.CheckWebhookURLs(") {
		t.Error("seedAutomation must pass its URLs through demorig.CheckWebhookURLs before sending")
	}
}

// The full scene passes "invalid" to `finish` so the destinations are checked a
// second time before any row is created.
func TestDemoSeedChecksTheFullScenesDestinationsBeforeCreatingThem(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "scripts", "demo-seed.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `finish "$FIXTURE" invalid`) {
		t.Error(`demo-seed.sh must run the full scene's finish with the "invalid" guard`)
	}
}

// The driver must never follow a redirect: Go re-sends a POST body on a 307 or
// 308, and /setup and /auth/login carry the password. A server that redirects
// every request is pointed at a second one; the second must see nothing.
func TestDemoDriverDoesNotFollowRedirects(t *testing.T) {
	var elsewhere, first atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	root := repoRootFromTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", filepath.Join(root, "scripts", "demo_seed_driver.go"),
		redirector.URL, "status", filepath.Join(root, "scripts", "demo-seed.sports.fixture.json"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "E2E_PASSWORD=not-a-real-password", "POLYEMESIS_SETUP_CODE=")
	_ = cmd.Run() // it is expected to fail: nothing answers as a polyemesis server
	if first.Load() == 0 {
		t.Fatal("the driver never reached the redirecting server")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the driver followed a redirect: %d request(s) reached the second server", n)
	}
}
