package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/db/dbtest"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// fakeToken is a placeholder, not a credential. It is long enough for the
// scrubber to act on and low-entropy enough for a secret scanner to ignore.
const fakeToken = "test-access-token-placeholder"

type rep struct {
	mu    sync.Mutex
	logs  []string
	prog  float64
	final any
}

func (r *rep) Progress(f float64) { r.mu.Lock(); r.prog = f; r.mu.Unlock() }
func (r *rep) Logf(format string, a ...any) {
	r.mu.Lock()
	r.logs = append(r.logs, fmt.Sprintf(format, a...))
	r.mu.Unlock()
}
func (r *rep) SetResult(v any) { r.final = v }

type env struct {
	t      *testing.T
	d      *db.DB
	box    *secrets.Box
	clip   File
	tokens int
	cred   Credential
	ad     map[string]Adapter
	gates  Gates
}

func newEnv(t *testing.T) *env {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{0x33}, 32))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "short.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("v"), 3000), 0o600); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, d: dbtest.OpenCheap(t), box: box, cred: Credential{AccessToken: fakeToken},
		clip: File{Path: path, Size: 3000, Seconds: 20, Vertical: true}, ad: map[string]Adapter{}}
}

func (e *env) claim(platform string) (db.Publication, Params) {
	e.t.Helper()
	p, out, err := e.d.ClaimPublication(db.Publication{
		ClipJobID: 11, Platform: platform, AccountID: 5, AccountName: "Chan", Title: "My short",
		Description: "d", Tags: []string{"fun"}, Privacy: "private", ContentHash: "h", Nonce: "nonce-click-1", ConfirmedBy: 1,
	})
	if err != nil || out != db.ClaimCreated {
		e.t.Fatalf("claim = %v %v", out, err)
	}
	return p, Params{PublicationID: p.ID, Nonce: "nonce-click-1"}
}

func (e *env) worker() jobs.Worker {
	return NewWorker(WorkerDeps{
		Ledger:   LedgerFor(e.d, e.box),
		Tokens:   func(context.Context, int64) (Credential, error) { e.tokens++; return e.cred, nil },
		Clips:    func(context.Context, int64) (File, error) { return e.clip, nil },
		Adapters: e.ad, Gates: e.gates,
	})
}

func (e *env) run(p Params, attempts int) (*rep, error) {
	e.t.Helper()
	raw, _ := json.Marshal(p)
	r := &rep{}
	err := e.worker().Run(context.Background(), jobs.Job{ID: 1, Kind: JobKind, Params: raw, Attempts: attempts, MaxAttempts: 3}, r)
	return r, err
}

func (e *env) row(id int64) db.Publication {
	e.t.Helper()
	p, err := e.d.GetPublication(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

// counting wraps a handler and records every request it served.
type counting struct {
	mu    sync.Mutex
	reqs  []string
	inner http.Handler
}

func (c *counting) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

func (c *counting) n() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.reqs) }
func (c *counting) count(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.reqs {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func serve(t *testing.T, h http.HandlerFunc) (*httptest.Server, *counting) {
	t.Helper()
	c := &counting{inner: h}
	s := httptest.NewServer(c)
	t.Cleanup(s.Close)
	return s, c
}

func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }

func mustParams(p Params) []byte {
	raw, _ := json.Marshal(p)
	return raw
}
