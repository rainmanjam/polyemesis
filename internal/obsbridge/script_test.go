package obsbridge_test

import (
	"bufio"
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
# What the script imports, so the interpreter's start-up cost is paid here and
# not inside the script's own time. The script itself -- anything its module
# body does -- is imported AFTER the marker, under the 60s.
import json, queue, ssl, threading, urllib.error, urllib.request
print("` + driverReady + `", flush=True)
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
	// -B: no __pycache__ beside the shipped script; -I: isolated from the
	// caller's environment and user site-packages.
	cmd := exec.Command(py, "-I", "-B", driver, stubDir, scriptDir, srvURL, token)
	cmd.Dir = t.TempDir()
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout // one *os.File, so both streams interleave in order
	if err := cmd.Start(); err != nil {
		t.Fatalf("python3 did not start: %v", err)
	}

	var (
		mu  sync.Mutex
		out strings.Builder
	)
	output := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
	ready := make(chan struct{})
	exited := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		seen := false
		for sc.Scan() {
			if !seen && sc.Text() == driverReady {
				// The marker is this test's, not the script's output.
				seen = true
				close(ready)
				continue
			}
			mu.Lock()
			out.WriteString(sc.Text() + "\n")
			mu.Unlock()
		}
		// A scanner that stopped on an error (an over-long line) has not
		// drained the pipe, and a child blocked writing to it never exits.
		_, _ = io.Copy(io.Discard, pipe)
		// Wait only after the pipe is drained; see exec.Cmd.StdoutPipe.
		exited <- cmd.Wait()
	}()
	// kill stops the child and waits for it, so nothing is still writing
	// output (or err) while the failure below reads it. Closing the pipe as
	// well means a handle inherited by anything the child started cannot hold
	// the reader open after the child itself is gone.
	kill := func() { _ = cmd.Process.Kill(); _ = pipe.Close(); <-exited }

	// PHASE ONE: THE INTERPRETER COMES UP. No wall clock of this test's own,
	// and that is deliberate. Starting CPython and importing urllib, ssl and
	// json is not the script's behaviour; it is the runner's disk and CPU. On
	// windows-latest it normally costs a few seconds, and on one runner whose
	// internal/db and internal/api packages ran 10-15x slow it cost more than
	// the whole 60s this test used to give the script -- both tests in the
	// package timed out together, and the driver's own join(20) shows the
	// script itself had not even been reached. Only the interpreter and the
	// standard library are in this phase; the script's module is imported
	// after the marker, inside the 60s.
	//
	// What bounds this phase is the test binary's -timeout, less room for the
	// whole of phase two and a margin to kill and report, so the test fails
	// with its child stopped and its phase named rather than as a goroutine
	// dump with a python process left running.
	var startup <-chan time.Time
	if dl, ok := t.Deadline(); ok {
		if room := time.Until(dl) - scriptBudget - 30*time.Second; room > 0 {
			startup = time.After(room)
		}
	}
	select {
	case <-ready:
	case err := <-exited:
		t.Fatalf("python exited before the interpreter was ready: %v\n%s", err, output())
	case <-startup:
		kill()
		t.Fatalf("python did not come up before the test binary's deadline left room "+
			"for the script\n%s", output())
	}

	// PHASE TWO: THE SCRIPT ITSELF, from its import to finished, and this is
	// the 60s that has always been here, unchanged. A script that hangs in its
	// module body, its callback, its worker or its unload still fails on it.
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the script failed: %v\n%s", err, output())
		}
	case <-time.After(scriptBudget):
		kill()
		t.Fatalf("the script did not finish within %v of the interpreter coming up\n%s",
			scriptBudget, output())
	}
	return output()
}

// driverReady is the line scriptDriver prints once python and the standard
// library are up, just before it imports the script: where the script's own
// time starts.
const driverReady = "polyemesis-test: interpreter ready"

// scriptBudget is the script's own time, from import to finished. The same 60s
// this test has always given it.
const scriptBudget = 60 * time.Second

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
