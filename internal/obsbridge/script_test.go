package obsbridge_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// THE OBS SCRIPT, RUN UNDER A STAND-IN FOR OBS. integrations/obs's script
// imports obspython, which exists only inside OBS; obspythonStub is enough of
// that module for the script to load, register its frontend callback and read
// its settings, and the driver then fires the three frontend events OBS would.
// What arrives at the httptest server is what polyemesis would receive.
//
// It proves the script's half of the contract -- path, method, header, body,
// and that a failure is reported without the token. It does not prove OBS
// fires these events as its documentation says; nothing here runs OBS.
//
// python3 is required, not optional: a test that skipped when it was missing
// would be counted by the skip census and would leave the script untested on
// exactly the machine where nobody noticed. CI's runners carry python3.

const obspythonStub = `
OBS_TEXT_DEFAULT = 0
OBS_TEXT_PASSWORD = 1
OBS_PATH_FILE = 0
OBS_FRONTEND_EVENT_STREAMING_STARTED = 2
OBS_FRONTEND_EVENT_STREAMING_STOPPED = 4
OBS_FRONTEND_EVENT_SCENE_CHANGED = 8
callbacks = []
current_scene = None
def obs_properties_create(): return {}
def obs_properties_add_text(p, k, d, t): p[k] = t
def obs_properties_add_path(p, k, d, t, f, x): p[k] = t
def obs_properties_add_bool(p, k, d): p[k] = "bool"
def obs_data_set_default_bool(s, k, v): s.setdefault(k, v)
def obs_data_get_string(s, k): return s.get(k, "")
def obs_data_get_bool(s, k): return bool(s.get(k, False))
def obs_frontend_add_event_callback(cb): callbacks.append(cb)
def obs_frontend_get_current_scene(): return current_scene
def obs_source_get_name(src): return src
def obs_source_release(src): pass
`

const scriptDriver = `
import sys
sys.path.insert(0, sys.argv[1])
sys.path.insert(0, sys.argv[2])
import obspython
import polyemesis_obs as s
settings = {"server": sys.argv[3], "token": sys.argv[4]}
s.script_defaults(settings)
s.script_update(settings)
props = s.script_properties()
assert props["token"] == obspython.OBS_TEXT_PASSWORD, "the token field must be a password field"
s.script_load(settings)
cb = obspython.callbacks[0]
cb(obspython.OBS_FRONTEND_EVENT_STREAMING_STARTED)
obspython.current_scene = "Just Chatting"
cb(obspython.OBS_FRONTEND_EVENT_SCENE_CHANGED)
cb(obspython.OBS_FRONTEND_EVENT_STREAMING_STOPPED)
cb(99)
s.RETRIES = 0
s.script_unload()
s._worker.join(20)
assert not s._worker.is_alive(), "the worker did not finish"
`

type recorded struct {
	method, path, auth, ctype string
	body                      map[string]any
}

func runScript(t *testing.T, srvURL, token string) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is not on PATH; the OBS script cannot be tested without it: %v", err)
	}
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "obspython.py"), []byte(obspythonStub), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(t.TempDir(), "driver.py")
	if err := os.WriteFile(driver, []byte(scriptDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	scriptDir, err := filepath.Abs("../../integrations/obs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, "-I", driver, stubDir, scriptDir, srvURL, token)
	cmd.Dir = t.TempDir()
	done := make(chan struct{})
	var out []byte
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the script did not finish")
	}
	if err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestTheOBSScriptPostsTheThreeFactsWithItsToken(t *testing.T) {
	var (
		mu  sync.Mutex
		got []recorded
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(srv.Close)
	token := "pe_" + strings.Repeat("o8", 12)

	out := runScript(t, srv.URL+"/", token)
	mu.Lock()
	defer mu.Unlock()
	want := []map[string]any{
		{"event": "stream.started"},
		{"event": "scene.changed", "scene": "Just Chatting"},
		{"event": "stream.stopped"},
	}
	if len(got) != len(want) {
		t.Fatalf("polyemesis received %d requests, want %d (the unknown frontend event must send nothing)\n%s", len(got), len(want), out)
	}
	for i, w := range want {
		g := got[i]
		if g.method != http.MethodPost || g.path != "/api/v1/obs/events" || g.ctype != "application/json" {
			t.Errorf("request %d = %s %s (%s)", i, g.method, g.path, g.ctype)
		}
		if g.auth != "Bearer "+token {
			t.Errorf("request %d carried Authorization %q", i, g.auth)
		}
		wb, _ := json.Marshal(w)
		gb, _ := json.Marshal(g.body)
		if string(wb) != string(gb) {
			t.Errorf("request %d body = %s, want %s", i, gb, wb)
		}
	}
	if strings.Contains(out, token) {
		t.Errorf("the script printed its token:\n%s", out)
	}
}

func TestTheOBSScriptReportsARefusalWithoutItsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"the OBS bridge is switched off"}`))
	}))
	t.Cleanup(srv.Close)
	token := "pe_" + strings.Repeat("r9", 12)
	out := runScript(t, srv.URL, token)
	if !strings.Contains(out, "HTTP 409") || !strings.Contains(out, "switched off") {
		t.Fatalf("the refusal was not reported:\n%s", out)
	}
	if strings.Contains(out, token) {
		t.Errorf("the script printed its token:\n%s", out)
	}
}
