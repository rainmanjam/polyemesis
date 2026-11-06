package engine

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A relay consumer is built one way (relayConsumerSpec), so the things every
// consumer needs -- the log and state callbacks, the log sink, and the wake that
// lets a child stopped on a quiet feed exit -- cannot be given to some and
// forgotten on others. WakeOnStop was on three of eleven.

// THE PREVIEW IS THE QUIET READER THAT ONCE WAS NOT WOKEN. Stopped after its
// publisher has left, with FFmpeg past its probe window, it ignored the single
// SIGTERM, sat out the whole grace and was SIGKILLed: the recorder and the
// destinations had the wake that prevents that, and the preview, renditions,
// playout variants, meters and selector feeds did not. Measured against real
// FFmpeg on the unfixed code: the stop took the full 8 s grace.
func TestAPreviewStoppedOnAQuietFeedIsWokenInsteadOfBeingKilled(t *testing.T) {
	ffmpegBin, _ := offsetBenchTools(t)
	fixture := quietStopFixture(t, ffmpegBin)

	e, _ := storeEngine(t)
	e.tools.FFmpeg = ffmpegBin
	e.alloc = relay.NewPortAllocator(freeUDPPort(t), 1)
	s := db.DefaultSettings()
	s.Preview.Enabled = true

	// A publisher that keeps the relay flowing, because the preview refuses to
	// start against a quiet one. Looped and fast, so stream time passes the
	// 15 s probe window within a few seconds of wall time.
	pub := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-stream_loop", "-1", "-readrate", "8", "-i", fixture, "-map", "0", "-c", "copy",
		"-f", "mpegts", "-flush_packets", "1",
		"udp://127.0.0.1:"+strconv.Itoa(e.hub.Port())+"?pkt_size=1316")
	if err := pub.Start(); err != nil {
		t.Fatalf("start the publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Process.Kill(); _ = pub.Wait() })

	for i := 0; i < 6; i++ {
		time.Sleep(250 * time.Millisecond)
		e.previewFlowing(time.Now())
	}
	e.previewMu.Lock()
	e.startPreviewLocked(s)
	e.mu.RLock()
	started := e.preview != nil
	e.mu.RUnlock()
	if !started {
		e.previewMu.Unlock()
		t.Fatal("the preview did not start against a flowing relay")
	}

	// Past the probe window in stream time, then the publisher leaves.
	time.Sleep(4 * time.Second)
	_ = pub.Process.Kill()
	_ = pub.Wait()
	time.Sleep(1500 * time.Millisecond)

	began := time.Now()
	e.stopPreviewLocked()
	took := time.Since(began)
	e.previewMu.Unlock()

	t.Logf("preview stop took %v", took)
	if took > 2*time.Second {
		t.Errorf("stopping the preview on a quiet feed took %v: it ignored SIGTERM and waited "+
			"out the grace to be killed, because nothing woke it", took)
	}
}

// THE BACKUP IS THE PRIMARY'S TWIN, and was built by a second hand-written Spec
// that lacked the #674 re-probe handler: a backup whose audio FFmpeg could not
// characterise (it probed before the audio existed) sat at "running" and
// published nothing for ever, where the primary restarts to probe again.
func TestABackupRestartsToReprobeWhenItsAudioCannotBeCharacterised(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake FFmpeg is a shell script")
	}
	e, _ := storeEngine(t)
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "ffmpeg")
	body := "#!/bin/sh\n" +
		"echo run >> " + runs + "\n" +
		"echo '[in#0/mpegts @ 0x1] Could not find codec parameters for stream 1 (Audio: aac ([15][0][0][0] / 0x000F), 0 channels): unspecified sample format' >&2\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	e.tools.FFmpeg = script

	d := &destination{row: backupRow(), hub: e.hub, spec: "spec"}
	e.buildBackup(d, routing.Result{}, "spec")
	if d.backup == nil {
		t.Fatalf("no backup was built: %s", d.backupErr)
	}
	d.backup.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.backup.Stop(ctx)
	})

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") >= 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	b, _ := os.ReadFile(runs)
	t.Fatalf("the backup ran %d time(s) and never restarted to re-probe, so a backup that probed before "+
		"its audio existed publishes nothing for ever", strings.Count(string(b), "run"))
}

// The #674 re-probe at the moment the ingest layout is first measured selects
// destinations that have never published. A backup is a second FFmpeg that can
// be holding the same stale characterisation, and it had no publishing signal
// to be selected by.
func TestABackupThatHasNeverPublishedIsReprobedWithItsPrimary(t *testing.T) {
	e, _ := storeEngine(t)
	row := backupRow()
	d := &destination{row: row, hub: e.hub, spec: "spec"}
	e.buildBackup(d, routing.Result{}, "spec")
	if d.backup == nil {
		t.Fatalf("no backup was built: %s", d.backupErr)
	}
	if d.backupWatch == nil {
		t.Fatal("the backup has no publishing signal, so nothing can tell a published backup from a stuck one")
	}
	primaryW := &destWatch{published: true}
	primary := &destination{row: row, proc: d.backup, watch: primaryW, backup: d.backup, backupWatch: d.backupWatch}
	got := destinationsNeedingReprobe(map[int64]*destination{row.ID: primary})
	if len(got) != 1 || got[0].w != d.backupWatch {
		t.Fatalf("a published primary with a backup that never published selected %d target(s), want the backup alone", len(got))
	}
}

// Every Spec in this package is built by relayConsumerSpec, except the three
// children that do not read a relay hub. A new relay consumer written as its own
// supervisor.Spec literal fails here until it is built the shared way -- which
// is how WakeOnStop was forgotten on eight of eleven.
func TestEveryRelayConsumerSpecComesFromTheSharedConstructor(t *testing.T) {
	notConsumers := map[string]string{
		"reconcileIngest":       "the ingest listens on the shared RTMP port or pulls a source; it reads no relay hub",
		"reconcileBackupIngest": "the backup ingest is the same kind of child as the ingest",
		"reconcilePlaylist":     "the playlist feed reads a concat list from disk and writes the playlist's own hub",
	}
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no source files found: %v", err)
	}
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Spec" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "supervisor" {
					return true
				}
				seen[fn.Name.Name] = true
				return true
			})
		}
	}
	for fn := range seen {
		if fn == "relayConsumerSpec" {
			continue
		}
		if _, ok := notConsumers[fn]; !ok {
			t.Errorf("%s builds a supervisor.Spec by hand. A child that reads a relay hub must come from "+
				"relayConsumerSpec, which sets the log and state callbacks, the log sink and the stop-time wake; "+
				"if this one reads no relay, add it to notConsumers with the reason", fn)
		}
	}
	if !seen["relayConsumerSpec"] {
		t.Fatal("the walker did not find relayConsumerSpec, so it checks nothing")
	}
}
