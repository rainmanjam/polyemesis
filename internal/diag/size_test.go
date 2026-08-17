package diag

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

/* THE RING BOUNDS HOW MANY RECORDS IT KEEPS AND, UNTIL NOW, NOTHING ABOUT HOW
 * LARGE THEY ARE.
 *
 * DefaultCapacity caps the count at 5,000. A Record holds an unbounded string
 * and an unbounded map of unbounded values, and Observe scrubbed both without
 * truncating either -- so "5,000 records" was a number the UI could report
 * honestly and a size nobody could predict. See #421.
 *
 * TWO THINGS ARE BEING ASSERTED, AND THEY ARE DIFFERENT CLAIMS:
 *
 *   the bundle is BOUNDED -- there exists a number of bytes it cannot exceed,
 *   and it is the record cap times the capacity
 *
 *   the bundle is HONEST ABOUT IT -- a record that was cut says so, in the
 *   record, and the capture states how many were cut. Silent truncation is the
 *   failure this whole feature exists to prevent: an engineer reading a
 *   quietly-shortened capture concludes the fault left no trace.
 *
 * The second matters more than the first. A bounded bundle that lies is worse
 * than an unbounded one that does not.
 */

func sizeRecorder(t *testing.T) *Recorder {
	t.Helper()
	r := NewRecorder(8, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	return r
}

// A SINGLE ENORMOUS RECORD CANNOT MAKE THE BUNDLE ENORMOUS.
func TestAnOversizedRecordIsCutToTheCap(t *testing.T) {
	r := sizeRecorder(t)
	huge := strings.Repeat("A", MaxRecordBytes*4)
	r.Observe(Record{Message: huge, Level: "INFO"})

	got := r.Records()
	if len(got) != 1 {
		t.Fatalf("held %d records, want 1", len(got))
	}
	if n := len(got[0].Message); n > MaxRecordBytes {
		t.Errorf("message kept %d bytes, cap is %d — the ring bounds the count "+
			"and must bound the size too", n, MaxRecordBytes)
	}
	// AND IT SAYS SO. A shortened line that reads as a complete one is how an
	// engineer concludes the fault left no trace.
	if !strings.Contains(got[0].Message, truncationMarker) {
		t.Errorf("a cut message does not carry %q, so nothing distinguishes it "+
			"from a line that was genuinely that short: %.80q",
			truncationMarker, got[0].Message)
	}
}

// THE CAP IS A BUDGET FOR THE WHOLE RECORD, not a per-string limit. A thousand
// attributes each just under a per-string cap would defeat one.
func TestManyAttributesCannotEvadeTheCapTogether(t *testing.T) {
	r := sizeRecorder(t)
	attrs := map[string]any{}
	for i := range 400 {
		attrs[string(rune('a'+i%26))+strings.Repeat("k", 8)+string(rune(i))] =
			strings.Repeat("v", 512)
	}
	r.Observe(Record{Message: "many attributes", Level: "INFO", Attrs: attrs})

	rec := r.Records()[0]
	if n := recordBytes(rec); n > MaxRecordBytes {
		t.Errorf("record kept %d bytes across %d attributes, cap is %d — a "+
			"per-string limit would have let this through", n, len(rec.Attrs), MaxRecordBytes)
	}
	if !rec.Truncated {
		t.Error("the record was cut and does not say so")
	}
}

// A NORMAL RECORD IS UNTOUCHED, which is the property that makes the cap safe
// to apply on the way in. An ffmpeg command line with a filter graph is the
// largest thing this box logs routinely, and cutting one would destroy the
// diagnostic the export exists to carry.
func TestARealisticRecordIsNotTouched(t *testing.T) {
	r := sizeRecorder(t)
	argv := []string{
		"/usr/local/bin/ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "warning",
		"-i", "udp://127.0.0.1:21000?fifo_size=5000&overrun_nonfatal=1",
		"-filter_complex",
		"[0:a:0]aresample=48000,aformat=channel_layouts=stereo[mt0];" +
			"[0:a:1]aresample=48000,aformat=channel_layouts=stereo[mt1];" +
			"[mt0][mt1]amerge=inputs=2[mgd];[mgd]astats=metadata=1:reset=1:length=0.1[mout]",
		"-map", "[mout]", "-f", "flv", "rtmp://live.example/app/key",
	}
	r.Observe(Record{Message: "starting destination", Level: "INFO",
		Attrs: map[string]any{"destination": "Main YouTube", "argv": argv}})

	rec := r.Records()[0]
	if rec.Truncated {
		t.Errorf("a routine ffmpeg command line (%d bytes) was cut; the cap is "+
			"meant to bound the pathological, not shorten the ordinary", recordBytes(rec))
	}
	if rec.Message != "starting destination" {
		t.Errorf("message = %q, want it intact", rec.Message)
	}
}

// THE CAP RUNS AFTER THE SCRUB, NEVER BEFORE.
//
// Truncating first would cut a credential in half and leave the front of it
// standing: the secret set matches WHOLE literals, so a key cut at the cap
// matches nothing any more and travels to a stranger as plaintext. This is the
// ordering bug the whole design is built to avoid.
//
// It uses disclosure_test.go's sentinelKey rather than a second fixture of its
// own. A near-copy is a second key-shaped literal for gitleaks to flag and a
// second entry the allowlist has to name -- and the first draft here typo'd one
// character of it, so the two files disagreed about what the sentinel was.
func TestTruncationCannotStrandHalfOfACredential(t *testing.T) {
	// CONSTRUCTED SO A CUT ALWAYS FIRES AND THE KEY SWEEPS ACROSS IT. Two
	// earlier versions of this test passed against a deliberately mis-ordered
	// implementation, which is a test that asserts nothing:
	//
	//   the first put the key at MaxRecordBytes-8, and the cut lands ~40 bytes
	//   earlier still because the marker reserves room -- so the key fell wholly
	//   outside the kept prefix and was never split
	//
	//   the second swept the offset but had no filler, so a cut only fired when
	//   the message BARELY exceeded the cap, which bounds the surviving prefix to
	//   about a dozen characters -- below the length worth calling a credential
	//
	// The trailing filler decouples the two: the cut point is fixed by the cap,
	// the key's position moves through it, and some offset strands a long prefix.
	filler := strings.Repeat("y", 20000)
	for pad := MaxRecordBytes - len(sentinelKey) - 96; pad < MaxRecordBytes; pad += 3 {
		if pad < 0 {
			continue
		}
		r := NewRecorder(8, alerts.NewSecretSet(nil, sentinelKey))
		r.SetRecording(true)
		r.Observe(Record{
			Message: strings.Repeat("x", pad) + sentinelKey + filler,
			Level:   "INFO",
		})

		got := r.Records()[0].Message
		// Any run of the key long enough to be recognisably a credential.
		for i := 12; i <= len(sentinelKey); i++ {
			if strings.Contains(got, sentinelKey[:i]) {
				t.Fatalf("at pad=%d a %d-character prefix of the credential survived "+
					"the cut: truncation ran BEFORE the scrub, so the set -- which "+
					"matches whole literals -- no longer recognised it, and it reaches "+
					"the bundle as plaintext", pad, i)
			}
		}
	}
}

// THE CAPTURE COUNTS WHAT IT CUT, so a bundle full of shortened lines is
// visibly that rather than silently that.
func TestTheCaptureReportsHowManyRecordsWereCut(t *testing.T) {
	r := sizeRecorder(t)
	r.Observe(Record{Message: "short", Level: "INFO"})
	r.Observe(Record{Message: strings.Repeat("B", MaxRecordBytes*2), Level: "INFO"})
	r.Observe(Record{Message: strings.Repeat("C", MaxRecordBytes*2), Level: "INFO"})

	b := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now())
	if b.Capture.RecordsTruncated != 2 {
		t.Errorf("capture says %d records were cut, want 2",
			b.Capture.RecordsTruncated)
	}
	if b.Capture.Bytes <= 0 {
		t.Error("the capture reports no size, so the dialog cannot state one")
	}
}

// THE REPORTED SIZE TRACKS THE RING, including eviction. A number that only
// ever grows would tell an operator their capture is enormous when the ring
// had long since dropped the records that made it so.
func TestTheReportedSizeFallsWhenRecordsAreEvicted(t *testing.T) {
	r := NewRecorder(4, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	for range 4 {
		r.Observe(Record{Message: strings.Repeat("D", 2000), Level: "INFO"})
	}
	big := r.Bytes()

	// Four small ones push every large record out of a ring of four.
	for range 4 {
		r.Observe(Record{Message: "tiny", Level: "INFO"})
	}
	if small := r.Bytes(); small >= big {
		t.Errorf("bytes went %d -> %d after the large records were evicted; the "+
			"total must fall with the ring, not accumulate", big, small)
	}
	if got := r.Bytes(); got > 4*MaxRecordBytes {
		t.Errorf("bytes = %d, which exceeds capacity*cap = %d", got, 4*MaxRecordBytes)
	}
}

// Reset clears the size with everything else.
func TestResetClearsTheSize(t *testing.T) {
	r := sizeRecorder(t)
	r.Observe(Record{Message: strings.Repeat("E", 1000), Level: "INFO"})
	if r.Bytes() == 0 {
		t.Fatal("fixture: nothing recorded")
	}
	r.Reset()
	if n := r.Bytes(); n != 0 {
		t.Errorf("bytes = %d after a reset, want 0", n)
	}
}

// bigErr and bigStringer are the shapes a log line reaches for when something
// upstream went wrong: a wrapped HTTP body, a driver error carrying a query.
type bigErr struct{ s string }

func (e bigErr) Error() string { return e.s }

type bigStringer struct{ s string }

func (b bigStringer) String() string { return b.s }

// THE BOUND IS A BOUND FOR EVERY SHAPE AN ATTRIBUTE CAN TAKE, not only the ones
// that happen to be strings.
//
// Table-driven rather than one test per shape, because the claim is universal:
// there is no record this ring accepts that exceeds the cap. A reviewer found
// three separate ways past the first implementation -- an error value, a
// oversized nested key, and the truncation marker itself being larger than the
// budget left for it -- and each was a different branch, so the useful assertion
// is the invariant, not the branches.
func TestNoRecordShapeCanExceedTheCap(t *testing.T) {
	huge := strings.Repeat("Z", MaxRecordBytes*3)
	nested := map[string]any{
		strings.Repeat("k", MaxRecordBytes*2): "v",
		"deep": map[string]any{
			strings.Repeat("j", MaxRecordBytes): strings.Repeat("w", MaxRecordBytes),
		},
	}

	for _, tc := range []struct {
		name string
		rec  Record
	}{
		{"an oversized message", Record{Level: "INFO", Message: huge}},
		{"an error value", Record{Level: "INFO", Message: "upstream refused",
			Attrs: map[string]any{"err": bigErr{huge}}}},
		{"a Stringer value", Record{Level: "INFO", Message: "state dump",
			Attrs: map[string]any{"state": bigStringer{huge}}}},
		{"a nested map with an enormous KEY", Record{Level: "INFO", Message: "cfg",
			Attrs: nested}},
		{"an argv of many long strings", Record{Level: "INFO", Message: "exec",
			Attrs: map[string]any{"argv": []string{huge, huge, huge}}}},
		// A SHAPE THE WALK DOES NOT RECOGNISE, reaching capRecord directly. In
		// the Observe path scrubValue renders these to strings first, so this
		// branch is only reachable here -- which is exactly why it was wrong:
		// priced at a flat 8 bytes, a 1 MiB []byte made the record measure about
		// sixteen and capRecord returned early believing it was already small.
		{"a raw []byte", Record{Level: "INFO", Message: "response",
			Attrs: map[string]any{"body": make([]byte, 1<<20)}}},
		{"a struct with large fields", Record{Level: "INFO", Message: "cfg",
			Attrs: map[string]any{"c": struct {
				A string `json:"a"`
				B string `json:"b"`
			}{huge, huge}}}},
		// The marker is ~30 bytes. A message that spends the budget down to a few
		// bytes leaves less room than the marker needs, and an implementation that
		// emits it anyway lands OVER the cap having tried to get under it.
		{"a message leaving less budget than the marker needs", Record{
			Level: "INFO", Message: strings.Repeat("m", MaxRecordBytes-8),
			Attrs: map[string]any{"k": huge}}},
		{"many attributes of every kind at once", Record{Level: "INFO", Message: huge,
			Attrs: map[string]any{
				"err": bigErr{huge}, "s": bigStringer{huge}, "argv": []string{huge},
				"n": 42, "ok": true, "nested": nested,
			}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, cut := capRecord(tc.rec)
			if n := recordBytes(got); n > MaxRecordBytes {
				t.Errorf("record kept %d bytes, cap is %d — the ceiling this feature "+
					"claims is not a ceiling for this shape", n, MaxRecordBytes)
			}
			// AND IT SAYS SO. A record cut without the flag reports a clean capture
			// and hands the engineer a line that stops for no stated reason.
			if !cut {
				t.Errorf("the record was over the cap and capRecord reported no cut")
			}
		})
	}
}

// A []string that lost elements SAYS an element was lost.
//
// Dropping the tail silently turns ["ffmpeg","-i","src","-f","flv","rtmp://…"]
// into ["ffmpeg","-i","src"], which reads as a command that was genuinely that
// short -- and the argv is usually the thing being diagnosed.
func TestADroppedArgvElementIsAnnounced(t *testing.T) {
	long := strings.Repeat("q", 3000)
	got, cut := capRecord(Record{Level: "INFO", Message: "exec",
		Attrs: map[string]any{"argv": []string{long, long, long, long, long}}})
	if !cut {
		t.Fatal("fixture: nothing was cut")
	}
	argv, ok := got.Attrs["argv"].([]string)
	if !ok {
		t.Fatalf("argv came back as %T", got.Attrs["argv"])
	}
	if len(argv) == 5 {
		t.Fatal("fixture: nothing was dropped")
	}
	if !strings.Contains(argv[len(argv)-1], truncationMarker) {
		t.Errorf("argv lost elements and the survivors say nothing about it: %.60q",
			argv[len(argv)-1])
	}
}

// AN UNRECOGNISED ATTRIBUTE SHAPE IS STILL SCRUBBED.
//
// FOUND BY CODEX, AND IT IS NOT A REGRESSION IN THIS CHANGE -- it is reachable
// on main. scrubValue handled string, []string, map[string]any, error and
// Stringer, and passed everything else through untouched. slog.Any("detail",
// map[string]string{...}) is everything else. The declared secret set never saw
// it, alerts.Redact never saw it, and it reached the bundle verbatim.
//
// The same gap is why the size cap was not binding: a value nobody could price
// was charged a flat 8 bytes, so a 1 MiB []byte measured as 16 and the record
// was waved through as already under the cap.
func TestAnUnrecognisedAttributeShapeIsStillScrubbedAndPriced(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  any
	}{
		{"a map of strings", map[string]string{"token": sentinelKey}},
		{"a slice of any", []any{"authorization", sentinelKey}},
		{"a struct", struct {
			Token string `json:"token"`
		}{sentinelKey}},
		{"a map of slices", map[string][]string{"argv": {"--key", sentinelKey}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRecorder(4, alerts.NewSecretSet(nil, sentinelKey))
			r.SetRecording(true)
			r.Observe(Record{Level: "INFO", Message: "publishing",
				Attrs: map[string]any{"detail": tc.val}})

			var buf bytes.Buffer
			if err := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now()).
				Encode(&buf); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if strings.Contains(buf.String(), sentinelKey) {
				t.Errorf("the declared credential reached the bundle verbatim inside "+
					"a %T — the scrub walk does not recognise this shape, so neither "+
					"the secret set nor the residual pass ever saw it", tc.val)
			}
		})
	}
}

// AND A LARGE UNRECOGNISED VALUE IS PRICED, so the cap actually engages.
func TestALargeUnrecognisedValueCannotEvadeTheCap(t *testing.T) {
	r := NewRecorder(4, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	// A []byte JSON-encodes to base64 — over a megabyte of it — while the old
	// flat 8-byte price made the whole record measure at about sixteen.
	r.Observe(Record{Level: "INFO", Message: "response body",
		Attrs: map[string]any{"body": make([]byte, 1<<20)}})

	var buf bytes.Buffer
	if err := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now()).
		Encode(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.Len() > 4*MaxRecordBytes {
		t.Errorf("one record encoded the bundle to %d bytes against a cap of %d "+
			"per record: an unrecognised value was never priced, so capRecord "+
			"returned early believing it was already small", buf.Len(), MaxRecordBytes)
	}
}

// AN OVERSIZED LEVEL CANNOT OVERDRAW THE BUDGET BEFORE THE MESSAGE IS PAID.
func TestAnOversizedLevelCannotBreakTheCap(t *testing.T) {
	got, cut := capRecord(Record{
		Level:   strings.Repeat("L", MaxRecordBytes*2),
		Message: "something happened",
	})
	if n := recordBytes(got); n > MaxRecordBytes {
		t.Errorf("record kept %d bytes, cap is %d — Level was subtracted from the "+
			"budget without ever being capped itself", n, MaxRecordBytes)
	}
	if !cut {
		t.Error("the record was over the cap and reported no cut")
	}
}

// THE CAPTURE AND THE RECORDS DESCRIBE THE SAME MOMENT.
//
// Build read Records(), Bytes(), Seen() and TruncatedCount() under four
// separate locks, so a concurrent Observe or Reset produced a bundle whose
// stated size belonged to a different ring state than its records. The number
// shown to an operator before they send the file has to be that file's number.
func TestTheCaptureAndTheRecordsAreOneSnapshot(t *testing.T) {
	r := NewRecorder(64, alerts.NewSecretSet(nil))
	r.SetRecording(true)

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				r.Observe(Record{Level: "INFO", Message: strings.Repeat("n", 200)})
			}
		}
	}()
	defer close(stop)

	for range 300 {
		b := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now())
		if b.Capture.Held != len(b.Records) {
			t.Fatalf("capture says %d held, bundle carries %d records — the two were "+
				"read under different locks", b.Capture.Held, len(b.Records))
		}
		// Sizes are measured, so the stated total must match what the records
		// actually weigh.
		sum := 0
		for _, rec := range b.Records {
			sum += recordBytes(rec)
		}
		if b.Capture.Bytes != sum {
			t.Fatalf("capture states %d bytes, the records weigh %d", b.Capture.Bytes, sum)
		}
	}
}

// Records() must not hand out the ring's own attribute maps: a caller mutating
// one changes a retained record while its recorded size does not move.
func TestRecordsDoesNotAliasTheRingsAttributes(t *testing.T) {
	r := NewRecorder(4, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	r.Observe(Record{Level: "INFO", Message: "m", Attrs: map[string]any{"k": "v"}})

	got := r.Records()
	got[0].Attrs["k"] = strings.Repeat("z", 50000)

	if again := r.Records(); again[0].Attrs["k"] != "v" {
		t.Error("a caller mutating the returned attributes changed the ring itself, " +
			"so the record no longer matches its recorded size")
	}
}

// THE WHOLE POINT, END TO END: the encoded bundle has a stateable ceiling.
func TestTheEncodedBundleIsBoundedByCapacityTimesTheCap(t *testing.T) {
	const capacity = 16
	r := NewRecorder(capacity, alerts.NewSecretSet(nil))
	r.SetRecording(true)
	for i := range capacity * 3 {
		r.Observe(Record{
			Message: strings.Repeat("F", MaxRecordBytes*2),
			Level:   "INFO",
			Attrs:   map[string]any{"i": i, "blob": strings.Repeat("G", MaxRecordBytes*2)},
		})
	}

	var buf bytes.Buffer
	if err := Build("v0", "linux/amd64", r, NewSwitch(slog.LevelInfo), time.Now()).Encode(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The envelope adds JSON syntax and indentation on top of the payload; the
	// claim is a bound that exists, not one that is tight.
	ceiling := capacity * MaxRecordBytes * 3
	if buf.Len() > ceiling {
		t.Errorf("bundle encoded to %d bytes against a ceiling of %d — before the "+
			"cap this was unbounded in the size of a single log line", buf.Len(), ceiling)
	}
	// And it is still valid JSON after being cut mid-string.
	var round Bundle
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("a truncated bundle is not decodable, so the engineer receiving "+
			"it cannot open it: %v", err)
	}
}
