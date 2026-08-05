package api

import (
	"context"
	"net/http"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// ------------------------------------------------------------------ parsing

func TestSplitExpertArgsAcceptsFFmpegSyntaxAndRejectsShellSyntax(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "empty string is no arguments", raw: "   ", want: nil},
		{
			name: "plain flags split on whitespace",
			raw:  "-muxdelay 0.1  -flvflags no_duration_filesize",
			want: []string{"-muxdelay", "0.1", "-flvflags", "no_duration_filesize"},
		},
		{
			name: "double quotes hold a value together",
			raw:  `-metadata "title=My Show"`,
			want: []string{"-metadata", "title=My Show"},
		},
		{
			name: "single quotes work the same way",
			raw:  `-metadata 'title=My Show'`,
			want: []string{"-metadata", "title=My Show"},
		},
		{
			name: "an empty quoted value survives as an empty argument",
			raw:  `-metadata ""`,
			want: []string{"-metadata", ""},
		},
		{
			// A backslash is a Windows path separator, not an escape. Treating
			// it as one would mangle every path on the platform this repo just
			// added support for.
			name: "backslashes are literal, so Windows paths survive",
			raw:  `-passlogfile C:\media\pass`,
			want: []string{"-passlogfile", `C:\media\pass`},
		},
		{
			// The optional-stream suffix, glob characters and filter-label
			// brackets are all real FFmpeg syntax. Refusing them would be the
			// restrictive-direction mistake, not a safety measure.
			name: "FFmpeg's own punctuation is not treated as shell syntax",
			raw:  `-map 0:a:1? -tag:v hvc1 -x264-params keyint=60:min-keyint=60`,
			want: []string{"-map", "0:a:1?", "-tag:v", "hvc1", "-x264-params", "keyint=60:min-keyint=60"},
		},
		{
			name:    "a bare semicolon is refused",
			raw:     "-f flv ; rm -rf /",
			wantErr: "shell metacharacter",
		},
		{name: "a pipe is refused", raw: "-f flv | tee out.flv", wantErr: "shell metacharacter"},
		{name: "a dollar sign is refused", raw: "-metadata title=$HOME", wantErr: "shell metacharacter"},
		{name: "a backtick is refused", raw: "-metadata title=`id`", wantErr: "shell metacharacter"},
		{name: "a redirect is refused", raw: "-f flv > out.flv", wantErr: "shell metacharacter"},
		{
			// Quoting is how a value legitimately contains one of these, and
			// the check is scoped to unquoted text so it stays possible.
			name: "a metacharacter inside quotes is a value, not syntax",
			raw:  `-metadata "title=Rock & Roll"`,
			want: []string{"-metadata", "title=Rock & Roll"},
		},
		{name: "an unclosed quote is refused", raw: `-metadata "title=oops`, wantErr: "unclosed"},
		{name: "a newline is refused", raw: "-f flv\n-y", wantErr: "control character"},
		{
			name:    "an over-long string is refused",
			raw:     strings.Repeat("a", ffmpeg.MaxExtraArgsChars+1),
			wantErr: "too long",
		},
		{
			name:    "too many arguments are refused",
			raw:     strings.TrimSpace(strings.Repeat("-x ", ffmpeg.MaxExtraArgsTokens+1)),
			wantErr: "limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitExpertArgs(tt.raw, "output args")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("splitExpertArgs(%q) = %v, want error containing %q", tt.raw, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				if !strings.HasPrefix(err.Error(), "output args:") {
					t.Errorf("error %q does not name the field it came from", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitExpertArgs(%q): %v", tt.raw, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitExpertArgs(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------- guardrails

func TestCheckExpertArgsSeparatesRefusalsFromAcknowledgeableOverrides(t *testing.T) {
	rendition := int64(7)

	tests := []struct {
		name       string
		in         []string
		out        []string
		renditerID *int64
		wantErr    string
		wantGuards []string // the flags expected to raise a guard
	}{
		{name: "nothing is always fine"},
		{
			name: "ordinary muxer flags need no ceremony",
			out:  []string{"-muxdelay", "0.1", "-flvflags", "no_duration_filesize"},
		},
		{
			// The generated command already says this. Repeating it changes
			// nothing, so demanding an acknowledgement would be theatre.
			name: "restating -c:v copy raises nothing",
			out:  []string{"-c:v", "copy"},
		},
		{
			name:       "re-encoding video on a passthrough destination needs an acknowledgement",
			out:        []string{"-c:v", "libx264"},
			wantGuards: []string{"-c:v"},
		},
		{
			name:       "the -c shorthand counts as a video codec override",
			out:        []string{"-c", "libx264"},
			wantGuards: []string{"-c"},
		},
		{
			name:       "so does the stream-indexed form",
			out:        []string{"-c:v:0", "libx264"},
			wantGuards: []string{"-c:v:0"},
		},
		{
			name:       "and the legacy spelling",
			out:        []string{"-vcodec", "libx264"},
			wantGuards: []string{"-vcodec"},
		},
		{
			// The rendition already encoded once; doing it again here is a
			// different problem with the same answer.
			name:       "overriding the codec behind a rendition needs one too",
			out:        []string{"-c:v", "libx264"},
			renditerID: &rendition,
			wantGuards: []string{"-c:v"},
		},
		{
			// The whole product is per-destination audio routing. A second
			// -map is not additive, it is a competing answer.
			name:       "-map is guarded because it can replace the routing graph",
			out:        []string{"-map", "0:a:0"},
			wantGuards: []string{"-map"},
		},
		{
			name:       "-filter_complex is guarded for the same reason",
			out:        []string{"-filter_complex", "[0:a:0]anull[aout]"},
			wantGuards: []string{"-filter_complex"},
		},
		{
			name:       "both sides of one edit are reported together",
			out:        []string{"-c:v", "libx264", "-map", "0:a:0"},
			wantGuards: []string{"-c:v", "-map"},
		},
		{
			// A second input renumbers every stream the routing graph names,
			// so [0:a:3] starts pointing somewhere else. No acknowledgement
			// covers that; it is a refusal.
			name:    "a second input is refused outright on the input side",
			in:      []string{"-i", "rtmp://elsewhere/live"},
			wantErr: "renumbers",
		},
		{
			name:    "and on the output side",
			out:     []string{"-i", "rtmp://elsewhere/live"},
			wantErr: "cannot be changed here",
		},
		{
			name:    "an output-side flag on the input side is refused with directions",
			in:      []string{"-map", "0:a:0"},
			wantErr: "belongs on the output side",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := &db.Destination{ID: 1, Kind: db.DestRTMP, RenditionID: tt.renditerID}
			guards, err := checkExpertArgs(tt.in, tt.out, row)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("checkExpertArgs = %v, want error containing %q", guards, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkExpertArgs: %v", err)
			}
			var got []string
			for _, g := range guards {
				got = append(got, g.Arg)
				if strings.TrimSpace(g.Reason) == "" {
					t.Errorf("guard for %s carries no reason", g.Arg)
				}
			}
			if !reflect.DeepEqual(got, tt.wantGuards) {
				t.Errorf("guards = %v, want %v", got, tt.wantGuards)
			}
		})
	}
}

// ------------------------------------------------------------------ splice

func TestSpliceExpertArgsPutsEachSideWhereFFmpegReadsIt(t *testing.T) {
	// The shape DestinationArgs produces: flags, -i INPUT, flags, TARGET.
	base := []string{"-hide_banner", "-i", "udp://relay", "-c:v", "copy", "-f", "flv", "rtmp://out/live"}

	tests := []struct {
		name string
		base []string
		in   []string
		out  []string
		want []string
	}{
		{name: "no additions leaves the command alone", base: base, want: base},
		{
			// Input options configure the input that FOLLOWS them. After -i
			// they are silently a no-op, which is the worst kind of wrong.
			name: "input args land immediately before -i",
			base: base,
			in:   []string{"-re"},
			want: []string{"-hide_banner", "-re", "-i", "udp://relay", "-c:v", "copy", "-f", "flv", "rtmp://out/live"},
		},
		{
			// Output options must precede the output URL, and the target must
			// stay last or FFmpeg reads it as an option value.
			name: "output args land immediately before the target",
			base: base,
			out:  []string{"-muxdelay", "0.1"},
			want: []string{"-hide_banner", "-i", "udp://relay", "-c:v", "copy", "-f", "flv", "-muxdelay", "0.1", "rtmp://out/live"},
		},
		{
			name: "both sides at once",
			base: base,
			in:   []string{"-re"},
			out:  []string{"-muxdelay", "0.1"},
			want: []string{"-hide_banner", "-re", "-i", "udp://relay", "-c:v", "copy", "-f", "flv", "-muxdelay", "0.1", "rtmp://out/live"},
		},
		{
			// Fail open. A command whose shape surprised us still gets shown
			// to the operator, who can read it and judge for themselves.
			name: "a command with no -i appends rather than refusing",
			base: []string{"-version"},
			in:   []string{"-re"},
			out:  []string{"-muxdelay", "0.1"},
			want: []string{"-version", "-re", "-muxdelay", "0.1"},
		},
		{
			name: "an empty base is not a panic",
			base: nil,
			out:  []string{"-y"},
			want: []string{"-y"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ffmpeg.SpliceExtraArgs(tt.base, tt.in, tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("spliceExpertArgs = %v\nwant %v", got, tt.want)
			}
		})
	}

	// The base must never be written through: when the destination is running
	// it IS the live process's own argv, and a splice that scribbled on it
	// would corrupt the command a supervisor restart re-executes.
	if !reflect.DeepEqual(base, []string{
		"-hide_banner", "-i", "udp://relay", "-c:v", "copy", "-f", "flv", "rtmp://out/live",
	}) {
		t.Errorf("spliceExpertArgs mutated the caller's slice: %v", base)
	}
}

func TestDryRunArgvReplacesTheOutputSoNothingIsPublished(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "the target is dropped and the output discarded",
			argv: []string{"-i", "udp://relay", "-f", "flv", "rtmp://live.example/app/key"},
			want: []string{"-i", "udp://relay", "-f", "flv", "-t", "1", "-f", "null", "-"},
		},
		{
			// The dry run reads FFmpeg's output to find the one line that
			// explains the failure. Progress key=value pairs would bury it.
			name: "the progress stream is stripped so the output stays readable",
			argv: []string{"-nostats", "-progress", "pipe:1", "-i", "udp://relay", "-f", "flv", "rtmp://out/k"},
			want: []string{"-nostats", "-i", "udp://relay", "-f", "flv", "-t", "1", "-f", "null", "-"},
		},
		{name: "an empty argv is not a panic", argv: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dryRunArgv(tt.argv)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dryRunArgv = %v, want %v", got, tt.want)
			}
			for _, a := range got {
				if strings.HasPrefix(a, "rtmp://") || strings.HasPrefix(a, "srt://") {
					t.Errorf("dry run would still publish to %q", a)
				}
			}
		})
	}
}

// A dry run may only say "invalid" when FFmpeg objected to an ARGUMENT.
// Anything else — an unreachable ingest above all — is inconclusive, because a
// check that is wrong in the restrictive direction is worse than no check.
func TestFindOptionErrorOnlyFiresOnArgumentComplaints(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "unrecognised option", output: "Unrecognized option 'preseat'.", want: true},
		{name: "unparseable value", output: "[AVOption] Unable to parse option value \"fast\"", want: true},
		{name: "missing filter", output: "No such filter: 'volumee'", want: true},
		{name: "unknown encoder", output: "Unknown encoder 'libx265x'", want: true},
		{name: "bad stream specifier", output: "Invalid stream specifier: a:9.", want: true},
		{
			name:   "a codec the container cannot carry",
			output: "[flv] Video codec vp9 not compatible with flv\nCould not write header for output file",
			want:   true,
		},
		{
			name:   "an unreachable ingest is not an argument problem",
			output: "udp://127.0.0.1:9000: Connection refused",
			want:   false,
		},
		{
			name:   "an empty input is not an argument problem",
			output: "Invalid data found when processing input",
			want:   false,
		},
		{
			name:   "a network timeout is not an argument problem",
			output: "rtmp://live.example/app: Operation timed out",
			want:   false,
		},
		{name: "silence is not an argument problem", output: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line, ok := findOptionError(tt.output)
			if ok != tt.want {
				t.Fatalf("findOptionError(%q) = %q,%v want ok=%v", tt.output, line, ok, tt.want)
			}
			if ok && line == "" {
				t.Error("reported an option error with no message to show")
			}
		})
	}
}

// A real FFmpeg, when one is installed, because the phrase list above is a
// claim about what FFmpeg actually prints and only FFmpeg can settle it.
func TestDryRunAgainstARealFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on PATH")
	}

	// A one-frame generated encode, so nothing here needs an ingest, a network
	// or a GPU. The trailing element stands in for the publish target that
	// dryRunArgv drops.
	base := []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-nostats", "-progress", "pipe:1",
		"-f", "lavfi", "-i", "testsrc2=size=64x48:rate=1",
		"-frames:v", "1", "-f", "null", "placeholder-target",
	}

	tests := []struct {
		name string
		out  []string
		want dryRunVerdict
	}{
		{name: "a command FFmpeg accepts", want: dryRunOK},
		{name: "a misspelt flag is caught", out: []string{"-preseat", "fast"}, want: dryRunInvalid},
		{name: "an encoder that does not exist is caught", out: []string{"-c:v", "libx264xx"}, want: dryRunInvalid},
		{name: "a filter that does not exist is caught", out: []string{"-vf", "scalee=32:24"}, want: dryRunInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runDryRun(context.Background(), bin, ffmpeg.SpliceExtraArgs(base, nil, tt.out))
			if got.Verdict != tt.want {
				t.Fatalf("verdict %q (%s), want %q\noutput: %s",
					got.Verdict, got.Message, tt.want, got.Output)
			}
			if got.Verdict == dryRunInvalid && got.Message == "" {
				t.Error("rejected the command without saying why")
			}
			// The progress stream would bury the one line that explains it.
			if strings.Contains(got.Output, "out_time_ms=") {
				t.Errorf("progress output leaked into the dry run report:\n%s", got.Output)
			}
		})
	}
}

// ------------------------------------------------------------------- routes

func TestEveryExpertRouteIsRegisteredAndRequiresAuth(t *testing.T) {
	h, _, _ := renditionServer(t, defaultTools())

	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/destinations/1/expert"},
		{http.MethodPut, "/api/v1/destinations/1/expert"},
		{http.MethodDelete, "/api/v1/destinations/1/expert"},
		{http.MethodPost, "/api/v1/destinations/1/expert/preview"},
		{http.MethodPost, "/api/v1/destinations/1/expert/dry-run"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := do(t, h, jsonRequest(t, tt.method, tt.path, nil))
			// A registered-but-unauthenticated route answers 401; one that was
			// never registered falls through to the SPA handler instead.
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401 (route missing?), body %s", w.Code, w.Body.String())
			}
		})
	}
}

// expertBody is the request shape all three write-ish endpoints take.
func expertBody(in, out string, ack, confirm bool) map[string]any {
	return map[string]any{
		"inputArgs": in, "outputArgs": out,
		"ackReencode": ack, "confirm": confirm,
	}
}

func TestExpertPreviewShowsTheWholeCommandAndSavesNothing(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	dest := createDestination(t, h, sign, destinationBody("preview", false, nil))
	path := "/api/v1/destinations/" + itoa(dest.ID) + "/expert"

	var resp expertResponse
	decodeInto(t, send(t, h, sign, http.MethodPost, path+"/preview",
		expertBody("-re", "-muxdelay 0.1", false, false), http.StatusOK), &resp)

	if resp.Applied {
		t.Error("a preview reported itself as applied")
	}
	// The whole point of the confirm step: the operator sees the real argv,
	// not a diff of the part they typed.
	for _, want := range []string{"-filter_complex", "-c:v", "copy", "-re", "-muxdelay"} {
		if !strings.Contains(resp.Command.Command, want) {
			t.Errorf("resolved command is missing %q:\n%s", want, resp.Command.Command)
		}
	}
	if !resp.Passthrough {
		t.Error("a destination with no rendition should report passthrough")
	}

	// Nothing was stored.
	var after expertResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &after)
	if after.Enabled || after.Args.OutputArgs != "" {
		t.Errorf("preview persisted %#v", after.Args)
	}
}

func TestExpertPutRefusesWithoutConfirmationAndWithoutAcknowledgement(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	dest := createDestination(t, h, sign, destinationBody("guarded", false, nil))
	path := "/api/v1/destinations/" + itoa(dest.ID) + "/expert"

	tests := []struct {
		name string
		body map[string]any
		want int
		msg  string
	}{
		{
			name: "harmless args still need the confirm step",
			body: expertBody("", "-muxdelay 0.1", false, false),
			want: http.StatusBadRequest,
			msg:  "confirm",
		},
		{
			name: "shell syntax is refused before anything else",
			body: expertBody("", "-f flv ; id", false, true),
			want: http.StatusBadRequest,
			msg:  "shell metacharacter",
		},
		{
			name: "re-encoding video needs the acknowledgement even when confirmed",
			body: expertBody("", "-c:v libx264", false, true),
			want: http.StatusBadRequest,
			msg:  "ackReencode",
		},
		{
			name: "a second input is refused however it is acknowledged",
			body: expertBody("-i rtmp://elsewhere/live", "", true, true),
			want: http.StatusBadRequest,
			msg:  "renumbers",
		},
		{
			name: "confirmed and acknowledged goes through",
			body: expertBody("", "-c:v libx264", true, true),
			want: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := send(t, h, sign, http.MethodPut, path, tt.body, tt.want)
			if tt.msg != "" && !strings.Contains(string(raw), tt.msg) {
				t.Errorf("response %s does not mention %q", raw, tt.msg)
			}
		})
	}
}

func TestExpertArgsSurviveAReadAndAreClearedByDelete(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	dest := createDestination(t, h, sign, destinationBody("roundtrip", false, nil))
	path := "/api/v1/destinations/" + itoa(dest.ID) + "/expert"

	send(t, h, sign, http.MethodPut, path,
		expertBody("-re", `-metadata "title=My Show"`, false, true), http.StatusOK)

	var got expertResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &got)
	if !got.Enabled {
		t.Fatal("saved arguments read back as disabled")
	}
	if got.Args.InputArgs != "-re" || got.Args.OutputArgs != `-metadata "title=My Show"` {
		t.Fatalf("read back %#v", got.Args)
	}
	// Quoting must survive the round trip as one argument, not two.
	if !strings.Contains(got.Command.Command, "'title=My Show'") {
		t.Errorf("quoted value did not survive into the command:\n%s", got.Command.Command)
	}

	send(t, h, sign, http.MethodDelete, path, nil, http.StatusOK)

	var cleared expertResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, path, nil, http.StatusOK), &cleared)
	if cleared.Enabled || cleared.Args.InputArgs != "" || cleared.Args.OutputArgs != "" {
		t.Fatalf("delete left %#v", cleared.Args)
	}
	if strings.Contains(cleared.Command.Command, "My Show") {
		t.Errorf("cleared command still carries the old arguments:\n%s", cleared.Command.Command)
	}
}

// Mutation: comment out `r.Get("/destinations/{id}/expert", s.handleGetExpert)`.
// The status-only version passed with the route gone whenever the UI had not
// been built, because the SPA fallback 404s too. See mustJSONError.
func TestExpertRejectsAnUnknownDestination(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	mustJSONError(t, h, sign, http.MethodGet, "/api/v1/destinations/999/expert", nil,
		http.StatusNotFound)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
