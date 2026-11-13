package testenv

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
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

// The seed driver is //go:build ignore, so no unit test can call it; these read
// its source (parsed, so a comment or a string cannot satisfy them).

type driverSource struct {
	text string
	file *ast.File
	fset *token.FileSet
}

func loadDriver(t *testing.T) driverSource {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), "scripts", "demo_seed_driver.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, raw, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse driver: %v", err)
	}
	return driverSource{text: string(raw), file: f, fset: fset}
}

// funcText returns the source text of the named top-level function.
func (d driverSource) funcText(t *testing.T, name string) string {
	t.Helper()
	for _, decl := range d.file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return d.text[d.fset.Position(fn.Pos()).Offset:d.fset.Position(fn.End()).Offset]
		}
	}
	t.Fatalf("scripts/demo_seed_driver.go has no func %s", name)
	return ""
}

func TestDemoDriverGuardsBeforeEveryStart(t *testing.T) {
	d := loadDriver(t)
	body := d.funcText(t, "startAll")
	guard := strings.Index(body, "guardLive(")
	start := strings.Index(body, `/start"`)
	if guard < 0 || start < 0 || guard > start {
		t.Errorf("startAll must call guardLive( before it builds a /start path (guardLive at %d, /start at %d)", guard, start)
	}
	// No other function may start a destination, and the bulk route would skip
	// the per-row guard above.
	if n := strings.Count(d.text, `/start"`); n != 1 {
		t.Errorf(`"/start" appears %d times in the driver, want exactly once (in startAll)`, n)
	}
	if strings.Contains(d.text, "start-all") {
		t.Error("the driver must not call the bulk start-all route; startAll guards then starts one by one")
	}
}

func TestDemoDriverNeverPrintsASecret(t *testing.T) {
	d := loadDriver(t)
	secret := map[string]bool{"password": true, "csrf": true, "setupCode": true}
	printers := map[string]bool{"Printf": true, "Fprintf": true, "Println": true, "Fprintln": true, "Print": true, "Fprint": true, "die": true}
	checked := 0
	ast.Inspect(d.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = f.Sel.Name
		case *ast.Ident:
			name = f.Name
		}
		if !printers[name] {
			return true
		}
		checked++
		for _, a := range call.Args {
			ast.Inspect(a, func(m ast.Node) bool {
				if id, ok := m.(*ast.Ident); ok && secret[id.Name] {
					t.Errorf("%s: %s( is given %s", d.fset.Position(id.Pos()), name, id.Name)
				}
				return true
			})
		}
		return true
	})
	if checked < 10 {
		t.Errorf("only %d print calls found; this guard has stopped matching", checked)
	}
	if !strings.Contains(d.funcText(t, "printSecrets"), "ModeCharDevice") {
		t.Error("printSecrets must refuse a terminal (os.ModeCharDevice)")
	}
}

func TestDemoDriverStopsAreConfirmed(t *testing.T) {
	if !strings.Contains(loadDriver(t).funcText(t, "stopAll"), `"confirm"`) {
		t.Error(`stopAll must send {"confirm": true}`)
	}
}

func TestDemoDriverRoutesByRole(t *testing.T) {
	// The behavioural check is the rig's receiver proof (plan 3, Task 13).
	if !strings.Contains(loadDriver(t).funcText(t, "routeAll"), `"commentary"`) {
		t.Error(`routeAll must choose tracks by the "commentary" role`)
	}
}

// The shell side of the same rule as TestDemoDriverNeverPrintsASecret: the demo
// password goes to $WORK/admin.pw and is read back from there, so no line that
// echoes or printf-s may expand it unless the line is the one that writes that
// file. An xtrace would print every expansion, so the scripts may not turn it on.
func TestDemoSeedNeverPrintsThePassword(t *testing.T) {
	root := repoRootFromTest(t)
	printer := regexp.MustCompile(`\b(echo|printf)\b`)
	expands := regexp.MustCompile(`\$\{?E2E_PASSWORD`)
	xtrace := regexp.MustCompile(`(^|[;&\s])set\s+-[a-zA-Z]*x|xtrace`)
	checked := 0
	for _, name := range []string{"demo-seed.sh", "lib-demo-sports.sh"} {
		raw, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			checked++
			if xtrace.MatchString(code) {
				t.Errorf("%s:%d turns on xtrace, which prints every expansion: %s", name, i+1, code)
			}
			if printer.MatchString(code) && expands.MatchString(code) && !strings.Contains(code, "admin.pw") {
				t.Errorf("%s:%d prints the password: %s", name, i+1, code)
			}
		}
	}
	if checked == 0 {
		t.Error("no script lines checked")
	}
}
