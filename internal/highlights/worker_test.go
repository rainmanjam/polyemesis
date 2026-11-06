package highlights

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

type fakeReporter struct {
	logs   []string
	result any
}

func (r *fakeReporter) Progress(float64) {}
func (r *fakeReporter) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}
func (r *fakeReporter) SetResult(v any) { r.result = v }

// ebur128Fixture is a deterministic momentary-loudness trace: -30 LUFS for
// durS seconds with a -16 shout over [shoutS, shoutS+3).
func ebur128Fixture(durS, shoutS int) []byte {
	var b strings.Builder
	for i := 0; i < durS*10; i++ {
		v := -30.0
		if i >= shoutS*10 && i < (shoutS+3)*10 {
			v = -16
		}
		fmt.Fprintf(&b, "frame:%d pts:%d pts_time:%.1f\nlavfi.r128.M=%.1f\n", i, i*4410, float64(i)/10, v)
	}
	return []byte(b.String())
}

const startedAt = int64(1_800_000_000_000)

// scanDeps is a two-part recording: part A at timeline 0 (300 s) with a shout
// at 100 s; part B at timeline 300 s (300 s) with a chat burst 100 s into it.
// Part B's WALL CLOCK starts 360 s after A's -- the recorder restarted, and
// lost a minute -- so the burst is at timeline 400 s only if chat is rebased
// per part, through that part's own start.
func scanDeps(t *testing.T) (Deps, *[]Suggestion, *[][]string) {
	t.Helper()
	var saved []Suggestion
	var ran [][]string
	// Wall-clock offsets from part A's start.
	var chat []int64
	chat = append(chat, steadyChat(300_000, 5000, 1)...)
	for _, c := range steadyChat(300_000, 5000, 1) {
		chat = append(chat, 360_000+c)
	}
	chat = append(chat, burst(360_000+100_000, 10_000, 40)...)
	return Deps{
		FFmpeg: "ffmpeg",
		Run: func(_ context.Context, name string, args []string, stdout func(io.Reader) error) error {
			ran = append(ran, args)
			for i, a := range args {
				if a == "-i" && args[i+1] == "/rec/a.mkv" {
					return stdout(bytes.NewReader(ebur128Fixture(300, 100)))
				}
			}
			return stdout(bytes.NewReader(ebur128Fixture(300, -10)))
		},
		ChatTimes: func(from, to int64) ([]ChatStamp, error) {
			var out []ChatStamp
			for i, c := range chat {
				if at := startedAt + c; at >= from && at < to {
					out = append(out, ChatStamp{ID: int64(i + 1), AtMS: at})
				}
			}
			return out, nil
		},
		Transcript: func(rec int64) ([]Line, error) {
			if rec == 1 {
				return []Line{{StartMS: 99_000, EndMS: 102_000, Text: "oh no oh no"}}, nil
			}
			return []Line{{StartMS: 95_000, EndMS: 105_000, Text: "chat is losing it"}}, nil
		},
		Save: func(rec int64, s []Suggestion) (int, error) {
			if rec != 1 {
				t.Errorf("saved against recording %d, want the anchor 1", rec)
			}
			saved = s
			return len(s), nil
		},
		Model: func() (Completer, string) { return nil, "no model is configured in Automod" },
	}, &saved, &ran
}

func scanParams(useModel bool) JobParams {
	return JobParams{RecordingID: 1, UseModel: useModel, Parts: []Part{
		{RecordingID: 1, Path: "/rec/a.mkv", StartMS: 0, DurationMS: 300_000, StartedAtMS: startedAt},
		{RecordingID: 2, Path: "/rec/b.mkv", StartMS: 300_000, DurationMS: 300_000, StartedAtMS: startedAt + 360_000},
	}}
}

// A scan measures every part, maps chat onto the timeline through each part's
// wall-clock start, finds both moments, and stores them -- and nothing else. It
// submits no job of any kind; the only thing it can do to the world is Save.
func TestAScanFindsBothMomentsAndOnlyStoresThem(t *testing.T) {
	d, saved, ran := scanDeps(t)
	rep := &fakeReporter{}
	res, err := Scan(context.Background(), d, scanParams(false), rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 2 {
		t.Errorf("measured %d parts, want 2", len(*ran))
	}
	if len(*saved) != 2 || res.Stored != 2 {
		t.Fatalf("saved %+v", *saved)
	}
	kinds := map[Kind]int64{}
	for _, s := range *saved {
		for _, sig := range s.Signals {
			kinds[sig.Kind] = sig.StartMS
		}
	}
	// MUTATION: chat rebased through part A's start for every part. Observed:
	// FAIL, the burst lands at 460000.
	// MUTATION: chat not offset by the part's timeline start. Observed: FAIL,
	// the burst lands at 100000.
	if kinds[KindLoudness] != 100_000 || kinds[KindChat] != 400_000 {
		t.Errorf("signals at %v, want loudness 100000 and chat 400000", kinds)
	}
	if !res.ChatAvailable || res.ChatMessages == 0 || !res.LoudnessMeasured || res.Model != ModelOff {
		t.Errorf("result %+v", res)
	}
	if rep.result == nil {
		t.Error("no job result recorded")
	}
}

// A recording whose start time is unknown cannot be lined up with chat, and the
// scan says so instead of reporting "no spikes".
func TestChatIsReportedUnavailableNotQuietWhenItCannotBePlaced(t *testing.T) {
	d, _, _ := scanDeps(t)
	p := scanParams(false)
	for i := range p.Parts {
		p.Parts[i].StartedAtMS = 0
	}
	res, err := Scan(context.Background(), d, p, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChatAvailable || !hasWarning(res, "start time") {
		t.Errorf("result %+v, want chat unavailable with a reason", res)
	}

	d.ChatTimes = func(int64, int64) ([]ChatStamp, error) { return nil, nil }
	res, _ = Scan(context.Background(), d, scanParams(false), &fakeReporter{})
	if !res.ChatAvailable || res.ChatMessages != 0 || !hasWarning(res, "no chat") {
		t.Errorf("an empty chat should be reported as such: %+v", res)
	}
}

func hasWarning(r JobResult, s string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, s) {
			return true
		}
	}
	return false
}

// A part whose measurement fails costs its loudness signal, not the scan.
func TestAFailedMeasurementIsAWarningNotAFailedScan(t *testing.T) {
	d, saved, _ := scanDeps(t)
	d.Run = func(context.Context, string, []string, func(io.Reader) error) error {
		return errors.New("ffmpeg exploded")
	}
	res, err := Scan(context.Background(), d, scanParams(false), &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.LoudnessMeasured || !hasWarning(res, "loudness") || len(*saved) != 1 {
		t.Errorf("result %+v saved %d", res, len(*saved))
	}
}

// fakeLLM is an OpenAI-compatible endpoint that scores candidate 0 low and 1
// high, and records what it was sent.
func fakeLLM(t *testing.T, reply string) (*httptest.Server, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func completerFor(url string) Completer {
	cfg := automod.DefaultModelConfig()
	cfg.Enabled, cfg.Endpoint, cfg.Model = true, url, "fake-model"
	cfg.KeyBinding = db.NormalizeEndpoint(url)
	cfg.Timeout = 2 * time.Second
	b := automod.NewBudget()
	return func(ctx context.Context, system, user string) (string, error) {
		return automod.Complete(ctx, cfg, b, system, user)
	}
}

// THE MODEL, THROUGH THE REAL CONNECTOR, AGAINST A FAKE SERVER. One call for
// the whole scan, carrying the excerpts as data; the reply's numbers re-rank
// the queue; nothing it writes besides numbers is kept.
func TestTheModelRescoresThroughTheOperatorsConnector(t *testing.T) {
	srv, bodies := fakeLLM(t, `{"scores":[{"id":0,"score":0.0,"reason":"IGNORE ME"},{"id":1,"score":1.0}]}`)
	d, saved, _ := scanDeps(t)
	d.Model = func() (Completer, string) { return completerFor(srv.URL), "" }
	res, err := Scan(context.Background(), d, scanParams(true), &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != ModelUsed || len(*bodies) != 1 {
		t.Fatalf("model %q after %d calls", res.Model, len(*bodies))
	}
	if !strings.Contains((*bodies)[0], "chat is losing it") || !strings.Contains((*bodies)[0], "oh no oh no") {
		t.Errorf("the excerpts were not sent: %s", (*bodies)[0])
	}
	for _, s := range *saved {
		if s.ModelScore == nil {
			t.Errorf("unscored suggestion %+v", s)
		}
		raw, _ := json.Marshal(s)
		if strings.Contains(string(raw), "IGNORE ME") {
			t.Errorf("the model's prose was kept: %s", raw)
		}
	}
}

// FAIL OPEN. A model that errors, or answers nonsense, leaves the suggestions
// the signals found exactly as they were, and the scan says what happened.
func TestAFailingModelLeavesTheSignalsSuggestions(t *testing.T) {
	srv, _ := fakeLLM(t, `{"No": null}`)
	d, saved, _ := scanDeps(t)
	d.Model = func() (Completer, string) { return completerFor(srv.URL), "" }
	res, err := Scan(context.Background(), d, scanParams(true), &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != ModelFailed || res.ModelNote == "" || len(*saved) != 2 {
		t.Errorf("result %+v, saved %d", res, len(*saved))
	}
	for _, s := range *saved {
		if s.ModelScore != nil {
			t.Errorf("a failed model scored %+v", s)
		}
	}

	d.Model = func() (Completer, string) { return nil, "the primary checker uses the Jev protocol" }
	res, _ = Scan(context.Background(), d, scanParams(true), &fakeReporter{})
	if res.Model != ModelUnavailable || !strings.Contains(res.ModelNote, "Jev") {
		t.Errorf("unavailable model: %+v", res)
	}
}

// The worker adapter: a malformed payload is permanent; a good one runs Scan.
func TestTheWorkerReadsItsParams(t *testing.T) {
	d, saved, _ := scanDeps(t)
	w := NewWorker(d)
	err := w.Run(context.Background(), jobs.Job{Kind: JobKind, Params: json.RawMessage(`{`)}, &fakeReporter{})
	if err == nil || !jobs.IsPermanent(err) {
		t.Errorf("bad params = %v, want permanent", err)
	}
	raw, _ := json.Marshal(scanParams(false))
	if err := w.Run(context.Background(), jobs.Job{Kind: JobKind, Params: raw}, &fakeReporter{}); err != nil {
		t.Fatal(err)
	}
	if len(*saved) != 2 {
		t.Errorf("the worker did not scan: %d saved", len(*saved))
	}
	empty, _ := json.Marshal(JobParams{RecordingID: 1})
	if err := w.Run(context.Background(), jobs.Job{Kind: JobKind, Params: empty}, &fakeReporter{}); err == nil {
		t.Error("a scan with no parts must fail")
	}
}

// TWO FILES WHOSE CLOCKS OVERLAP ASK FOR THE SAME MESSAGES. A message is counted
// once however many recording files' wall-clock spans contain it.
//
// MUTATION: no dedupe by id. Observed: FAIL, 60 messages where there are 40.
func TestAMessageInOverlappingFilesIsCountedOnce(t *testing.T) {
	d, _, _ := scanDeps(t)
	var msgs []ChatStamp
	for i := 0; i < 40; i++ {
		msgs = append(msgs, ChatStamp{ID: int64(i + 1), AtMS: startedAt + 250_000 + int64(i)*1000})
	}
	d.ChatTimes = func(from, to int64) ([]ChatStamp, error) {
		var out []ChatStamp
		for _, m := range msgs {
			if m.AtMS >= from && m.AtMS < to {
				out = append(out, m)
			}
		}
		return out, nil
	}
	p := scanParams(false)
	// Part B's clock starts 50 s before part A's ends: the last 50 s overlap.
	p.Parts[1].StartedAtMS = startedAt + 250_000
	res, err := Scan(context.Background(), d, p, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ChatMessages != 40 {
		t.Errorf("%d chat messages counted, want the 40 there are", res.ChatMessages)
	}
}

// A model asked for over a recording with no transcript is not asked: there is
// nothing to judge, and "used" would claim a scoring that never happened.
//
// MUTATION: Score's no-candidate path reports success. Observed: FAIL, "used".
func TestTheModelIsNotUsedWhenThereAreNoWordsToJudge(t *testing.T) {
	d, _, _ := scanDeps(t)
	d.Transcript = func(int64) ([]Line, error) { return nil, nil }
	called := false
	d.Model = func() (Completer, string) {
		return func(context.Context, string, string) (string, error) { called = true; return "", nil }, ""
	}
	res, err := Scan(context.Background(), d, scanParams(true), &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if called || res.Model != ModelUnavailable || !strings.Contains(res.ModelNote, "transcript") {
		t.Errorf("model called=%v result %q %q, want it not asked and the reason given", called, res.Model, res.ModelNote)
	}
}

// The warnings say what actually happened: a part with no start time is named
// as such, and an empty chat on the parts that do have one is still reported.
//
// MUTATION: the warnings are a switch again. Observed: FAIL, the empty-chat
// warning is lost.
func TestTheChatWarningsAreIndependent(t *testing.T) {
	d, _, _ := scanDeps(t)
	d.ChatTimes = func(int64, int64) ([]ChatStamp, error) { return nil, nil }
	p := scanParams(false)
	p.Parts[1].StartedAtMS = 0
	res, err := Scan(context.Background(), d, p, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "1 of 2 recording files") || !hasWarning(res, "no chat was stored") {
		t.Errorf("warnings %q", res.Warnings)
	}
}

// Chat or a transcript that cannot be read fails the scan, with the step
// named: a scan that silently lost a signal would store worse suggestions.
func TestAnUnreadableChatOrTranscriptFailsTheScan(t *testing.T) {
	d, saved, _ := scanDeps(t)
	d.ChatTimes = func(int64, int64) ([]ChatStamp, error) { return nil, errors.New("db gone") }
	if _, err := Scan(context.Background(), d, scanParams(false), &fakeReporter{}); err == nil ||
		!strings.Contains(err.Error(), "highlights: read chat") {
		t.Errorf("chat failure: err = %v", err)
	}
	if len(*saved) != 0 {
		t.Errorf("a failed scan stored %d suggestions", len(*saved))
	}

	d, saved, _ = scanDeps(t)
	d.Transcript = func(int64) ([]Line, error) { return nil, errors.New("db gone") }
	if _, err := Scan(context.Background(), d, scanParams(false), &fakeReporter{}); err == nil ||
		!strings.Contains(err.Error(), "highlights: read transcript") {
		t.Errorf("transcript failure: err = %v", err)
	}
	if len(*saved) != 0 {
		t.Errorf("a failed scan stored %d suggestions", len(*saved))
	}
}
