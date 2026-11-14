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
	"runtime"
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
	for _, field := range []string{"URL", "BackupURL", "StreamKey", "BackupStreamKey", "ExtraInputArgs", "ExtraOutputArgs", "AccountID", "Multitrack", "Kind"} {
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

// The legacy seeder (scripts/seed_demo.go) POSTs the password to /setup and
// /auth/login, so it must not follow a redirect either.
func TestSeedDemoDoesNotFollowRedirects(t *testing.T) {
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
	port := redirector.URL[strings.LastIndex(redirector.URL, ":")+1:]

	root := repoRootFromTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", filepath.Join(root, "scripts", "seed_demo.go"), port)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "E2E_PASSWORD=not-a-real-password", "POLYEMESIS_SETUP_CODE=")
	_ = cmd.Run() // expected to fail: nothing answers as a polyemesis server
	if first.Load() == 0 {
		t.Fatal("the seeder never reached the redirecting server")
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the seeder followed a redirect: %d request(s) reached the second server", n)
	}
}

// The legacy capture scripts keep their work directory in $TMPDIR, outside the
// module, and seed_demo.go imports internal/demorig, so `cd $WORK && go run`
// fails there with "go.mod file not found". The scripts build the seeder with
// scripts/build-seeder.sh; this runs that step from a directory outside the
// repository and expects a binary.
func TestLegacyCaptureScriptsCanBuildTheSeederFromOutsideTheModule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the capture scripts are bash and Docker and are not run on Windows; build-seeder.sh takes a POSIX output path")
	}
	root := repoRootFromTest(t)
	outside := t.TempDir()
	if strings.HasPrefix(outside, root) {
		t.Skipf("the temp directory %s is inside the repository", outside)
	}
	bin := filepath.Join(outside, "seed_demo")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "build-seeder.sh"), bin)
	cmd.Dir = outside
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the seeder from outside the module failed: %v\n%s", err, out)
	}
	if st, err := os.Stat(bin); err != nil || st.Size() == 0 {
		t.Fatalf("no seeder binary at %s: %v", bin, err)
	}
	// And the scripts use that step, not the old invocation.
	for _, name := range []string{"capture-lanes.sh", "capture-media.sh"} {
		raw, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		txt := string(raw)
		if !strings.Contains(txt, "build-seeder.sh") {
			t.Errorf("%s does not build the seeder with build-seeder.sh", name)
		}
		if regexp.MustCompile(`(?m)^[^#\n]*go run [^\n]*seed_demo\.go`).MatchString(txt) {
			t.Errorf("%s still runs the seeder with go run from the work directory", name)
		}
	}
}
