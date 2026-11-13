package testenv

import (
	"bytes"
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

type sportsTrack struct {
	Label    string `json:"label"`
	Role     string `json:"role"`
	Language string `json:"language"`
}

type sportsProfile struct {
	Mode   string `json:"mode"`
	Tracks []struct {
		Track   int     `json:"track"`
		Enabled bool    `json:"enabled"`
		Gain    float64 `json:"gain"`
	} `json:"tracks"`
	Normalize    string   `json:"normalize"`
	SampleRate   int      `json:"sampleRate"`
	ExcludeRoles []string `json:"excludeRoles"`
	Loudness     struct {
		TargetLufs float64 `json:"targetLufs"`
		TruePeakDb float64 `json:"truePeakDb"`
	} `json:"loudness"`
}

type sportsFixture struct {
	Scene      string            `json:"scene"`
	Settings   json.RawMessage   `json:"settings"`
	Recordings []json.RawMessage `json:"recordings"`
	Sources    []struct {
		Name         string                     `json:"name"`
		Video        map[string]json.RawMessage `json:"video"`
		Audio        []sportsTrack              `json:"audio"`
		Renditions   []json.RawMessage          `json:"renditions"`
		Destinations []struct {
			Name         string `json:"name"`
			Kind         string `json:"kind"`
			Platform     string `json:"platform"`
			URL          string `json:"url"`
			BackupURL    string `json:"backupUrl"`
			StreamKey    string `json:"streamKey"`
			Enabled      bool   `json:"enabled"`
			AudioBitrate int    `json:"audioBitrate"`
			// Present so the strict decode accepts them; the test asserts they are unset.
			ExtraInputArgs  string        `json:"extraInputArgs"`
			ExtraOutputArgs string        `json:"extraOutputArgs"`
			AccountID       *int64        `json:"accountId"`
			Multitrack      bool          `json:"multitrack"`
			Profile         sportsProfile `json:"profile"`
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
	// Strict: a field this test does not know about fails here, so a new field
	// cannot pass unseen.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var fx sportsFixture
	if err := dec.Decode(&fx); err != nil {
		t.Fatalf("parse sports fixture: %v", err)
	}
	return fx, root
}

func TestSportsFixtureIsOneProgrammeFiveLocalDestinations(t *testing.T) {
	fx, _ := loadSportsFixture(t)
	if fx.Scene != "sports" {
		t.Errorf("scene = %q, want sports", fx.Scene)
	}
	if len(fx.Recordings) != 0 {
		t.Errorf("recordings = %d, want none", len(fx.Recordings))
	}
	if len(fx.Sources) != 1 {
		t.Fatalf("want one source, got %d", len(fx.Sources))
	}
	src := fx.Sources[0]
	if len(src.Renditions) != 0 {
		t.Errorf("renditions = %d, want none", len(src.Renditions))
	}
	if _, has := src.Video["lavfi"]; has {
		t.Error("video must not carry lavfi: the media is scripts/demo-sports/")
	}
	wantTracks := []sportsTrack{
		{"English", "commentary", "en"}, {"Español", "commentary", "es"}, {"Português", "commentary", "pt"},
		{"Français", "commentary", "fr"}, {"Arabic", "commentary", "ar"}, {"Crowd", "other", ""},
	}
	if len(src.Audio) != len(wantTracks) {
		t.Fatalf("audio tracks = %d, want %d", len(src.Audio), len(wantTracks))
	}
	for i, w := range wantTracks {
		if src.Audio[i] != w {
			t.Errorf("audio %d = %+v, want %+v", i, src.Audio[i], w)
		}
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
		if d.URL != "rtmp://"+sportsReceiverHost+":1935/live" {
			t.Errorf("%s: url = %q", d.Name, d.URL)
		}
		if d.AudioBitrate != 160 {
			t.Errorf("%s: audioBitrate = %d, want 160", d.Name, d.AudioBitrate)
		}
		// The key carries the language of its own destination (en for English ...).
		code := wantTracks[i].Language
		if d.StreamKey != "demo-local-"+code+"-not-a-real-key" {
			t.Errorf("%s: stream key does not carry %q", d.Name, code)
		}
		p := d.Profile
		if p.Mode != "simple" || p.Normalize != "loudnorm" || p.SampleRate != 48000 ||
			p.Loudness.TargetLufs != -14 || p.Loudness.TruePeakDb != -1 ||
			len(p.ExcludeRoles) != 1 || p.ExcludeRoles[0] != "music" {
			t.Errorf("%s: profile = %+v", d.Name, p)
		}
		if len(p.Tracks) != 6 {
			t.Errorf("%s: profile has %d tracks, want 6", d.Name, len(p.Tracks))
		}
		for j, tr := range p.Tracks {
			if tr.Track != j || !tr.Enabled || tr.Gain != 1 {
				t.Errorf("%s: profile track %d = %+v, want enabled at gain 1", d.Name, j, tr)
			}
		}
		if d.ExtraInputArgs != "" || d.ExtraOutputArgs != "" || d.AccountID != nil || d.Multitrack {
			t.Errorf("%s carries extra args, an account or multitrack", d.Name)
		}
		rows = append(rows, demorig.DestRow{Name: d.Name, URL: d.URL, BackupURL: d.BackupURL,
			ExtraInputArgs: d.ExtraInputArgs, ExtraOutputArgs: d.ExtraOutputArgs, AccountID: d.AccountID, Multitrack: d.Multitrack})
	}
	if err := demorig.CheckStored(rows, []string{sportsReceiverHost}); err != nil {
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
	voices, err := os.ReadFile(filepath.Join(root, "scripts", "demo-sports", "voices.py"))
	if err != nil {
		t.Fatalf("read voices.py: %v", err)
	}
	var pitches []string
	for _, m := range regexp.MustCompile(`\((\d+),`).FindAllStringSubmatch(string(voices), -1) {
		pitches = append(pitches, m[1])
	}
	if len(pitches) != 5 {
		t.Fatalf("read %d pitches from voices.py, want 5", len(pitches))
	}
	for _, hz := range pitches {
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
	// One read, guarded and then used: a second listStored( inside startAll would
	// start rows that were added or edited after the guard looked.
	if strings.Contains(body, "listStored(") {
		t.Error("startAll must not read the destination list itself; start only the rows guardLive returned")
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
	ps := d.funcText(t, "printSecrets")
	if !strings.Contains(ps, "ModeCharDevice") || !strings.Contains(ps, "IsRegular()") {
		t.Error("printSecrets must refuse a terminal and a pipe: stdout has to be a regular file")
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

// A response body can carry a stream key (GET /destinations begins with id,
// name, kind, platform, url, streamKey), so no failure path may print one: only
// method, path, status and length.
func TestDemoDriverNeverEchoesAResponseBody(t *testing.T) {
	d := loadDriver(t)
	bodyNames := map[string]bool{"body": true, "out": true, "raw": true, "b": true, "resp": true, "data": true, "text": true}
	printers := map[string]bool{"Printf": true, "Fprintf": true, "Println": true, "Fprintln": true, "Print": true, "Fprint": true, "die": true}
	checked := 0
	var walk func(n ast.Node, printer string)
	walk = func(n ast.Node, printer string) {
		ast.Inspect(n, func(m ast.Node) bool {
			switch x := m.(type) {
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "len" {
					return false // a length is not the content
				}
			case *ast.Ident:
				if bodyNames[x.Name] {
					t.Errorf("%s: %s( is given %s, which holds a response body or a secret-bearing buffer", d.fset.Position(x.Pos()), printer, x.Name)
				}
			}
			return true
		})
	}
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
		if printers[name] {
			checked++
			for _, a := range call.Args {
				walk(a, name)
			}
		}
		return true
	})
	if checked < 10 {
		t.Errorf("only %d print calls found; this guard has stopped matching", checked)
	}
	if strings.Contains(d.text, "func truncate(") {
		t.Error("truncate echoes part of a body; delete it")
	}
}

// The refusal of platform accounts, multitrack and extra arguments is decided
// in internal/demorig (where it is unit-tested); the driver only fetches and
// calls. guardLive must therefore hold no decision of its own about those fields.
func TestDemoDriverDelegatesTheStoredRowCheck(t *testing.T) {
	gl := loadDriver(t).funcText(t, "guardLive")
	if !strings.Contains(gl, "demorig.CheckStored(") {
		t.Error("guardLive must call demorig.CheckStored")
	}
	for _, f := range []string{"ExtraOutputArgs", "ExtraInputArgs", "AccountID", "Multitrack", "switch"} {
		if strings.Contains(gl, f) {
			t.Errorf("guardLive mentions %s: that decision belongs in demorig.CheckStored", f)
		}
	}
}

// The shell side of the same rule as TestDemoDriverNeverPrintsASecret: the demo
// password, the setup code, the publish token and the stream keys go to files
// in $WORK and are read back from there. A statement that echoes or printf-s
// (joined across backslash continuations) may expand one of them only when its
// output is redirected to a file; `>&2` or no redirect is a print. An xtrace
// would print every expansion, so the scripts may not turn it on.
func TestDemoSeedNeverPrintsThePassword(t *testing.T) {
	root := repoRootFromTest(t)
	printer := regexp.MustCompile(`\b(echo|printf)\b`)
	secret := regexp.MustCompile(`\$\{?(E2E_PASSWORD|POLYEMESIS_SETUP_CODE|PUB_TOKEN|PROOF_URL|token|TOKEN|key)\b|` +
		`\bcat\b[^|;&]*(admin\.pw|setup\.code|secrets\.list)`)
	toFile := regexp.MustCompile(`(^|[^&\d])\d?>>?\s*"?\$\{?(WORK|dir|f)\b`)
	splitCommands := regexp.MustCompile(`\|\||&&|;|\{|\}`)
	xtrace := regexp.MustCompile(`(^|[;&\s])set\s+-[a-zA-Z]*x|xtrace`)
	checked, printers := 0, 0
	for _, name := range []string{"demo-seed.sh", "lib-demo-sports.sh"} {
		raw, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var stmt strings.Builder
		start := 0
		for i, line := range strings.Split(string(raw), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			checked++
			if xtrace.MatchString(code) {
				t.Errorf("%s:%d turns on xtrace, which prints every expansion: %s", name, i+1, code)
			}
			if stmt.Len() == 0 {
				start = i + 1
			}
			if strings.HasSuffix(code, "\\") {
				stmt.WriteString(strings.TrimSuffix(code, "\\") + " ")
				continue
			}
			stmt.WriteString(code)
			text := stmt.String()
			stmt.Reset()
			// One command at a time: `[ -n "$token" ] || { echo "no token" >&2; }`
			// tests a credential and prints a constant.
			for _, cmd := range splitCommands.Split(text, -1) {
				if !printer.MatchString(cmd) {
					continue
				}
				printers++
				if secret.MatchString(cmd) && !toFile.MatchString(cmd) {
					t.Errorf("%s:%d prints a credential: %s", name, start, strings.TrimSpace(cmd))
				}
			}
		}
	}
	if checked == 0 || printers == 0 {
		t.Errorf("nothing checked (lines %d, printing statements %d)", checked, printers)
	}
}
