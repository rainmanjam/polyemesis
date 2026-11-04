package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* AN FFMPEG ARGV CAN READ AND WRITE FILES ON THE SERVER (poka-yoke audit
 * 2026-11-04, finding 2). `-progress <db path>` overwrote the database, a
 * second positional output created a file anywhere the service could write,
 * and movie=/textfile= read a server file out to the platform. None of that is
 * a stream setting, so it is refused outright -- no acknowledgement covers it.
 */
func TestExpertModeRefusesArgumentsThatTouchServerFiles(t *testing.T) {
	row := &db.Destination{ID: 1, Kind: db.DestRTMP}
	for _, tc := range []struct {
		name string
		in   []string
		out  []string
		want string
	}{
		{"progress overwrites a file", nil, []string{"-progress", "/var/lib/polyemesis/polyemesis.db"}, "-progress"},
		{"vstats_file writes one", nil, []string{"-vstats_file", "/tmp/x"}, "-vstats_file"},
		{"vstats writes one in the cwd", nil, []string{"-vstats"}, "-vstats"},
		{"passlogfile writes one", nil, []string{"-passlogfile", "/tmp/x"}, "-passlogfile"},
		{"a stream specifier does not hide it", nil, []string{"-passlogfile:v", "/tmp/x"}, "-passlogfile"},
		{"dump_attachment writes on the input side", []string{"-dump_attachment:t", "/tmp/x"}, nil, "-dump_attachment"},
		{"attach reads a file out", nil, []string{"-attach", "/etc/hostname"}, "-attach"},
		{"stats_enc_pre writes one", nil, []string{"-stats_enc_pre:v", "/tmp/x"}, "-stats_enc_pre"},
		{"filter_complex_script reads one", nil, []string{"-filter_complex_script", "/tmp/g"}, "-filter_complex_script"},
		{"a second positional output", nil, []string{"-c:a", "aac", "/var/lib/polyemesis/evil.flv"}, "another output"},
		{"a positional after a boolean", nil, []string{"-an", "/tmp/evil.flv"}, "another output"},
		{"a positional on the input side", []string{"/tmp/evil.flv"}, nil, "another output"},
		{"stdout as an output", nil, []string{"-"}, "another output"},
		{"a dangling option swallows the destination URL", nil, []string{"-metadata"}, "needs a value"},
		{"movie= reads a media file", nil, []string{"-vf", "movie=clip.mp4[w];[in][w]overlay"}, "movie"},
		{"amovie= in a complex graph", nil, []string{"-filter_complex", "amovie=a.wav[x]"}, "amovie"},
		{"subtitles= reads a file", nil, []string{"-vf", "subtitles=subs.srt"}, "subtitles"},
		{"drawtext textfile= reads a file", nil, []string{"-vf", "drawtext=textfile=notes.txt:x=1"}, "drawtext"},
		{"metadata file= writes one", nil, []string{"-af", "ametadata=mode=print:file=out.log"}, "ametadata"},
		// The bypasses a security review found in the first, denylist, version.
		// Relative paths on purpose: they write into FFmpeg's working directory
		// just as well, and they keep the /option= check from also catching
		// these, so each case tests the filter allowlist on its own.
		{"psnr stats_file given positionally", nil, []string{"-filter_complex", "split[a][b];[a][b]psnr=polyemesis.db"}, "psnr"},
		{"psnr stats_file by its short alias", nil, []string{"-filter_complex", "split[a][b];[a][b]psnr=f=stats.log"}, "psnr"},
		{"a filter nobody listed", nil, []string{"-vf", "removelogo=mask.png"}, "removelogo"},
		{"an allowed filter loading a value from a file", nil, []string{"-af", "volume=/volume=/etc/passwd"}, "/option="},
		{"an unknown option followed by an output path", nil, []string{"-made_up_boolean", "/tmp/evil.flv"}, "-made_up_boolean"},
		// The bypasses the second security review found: the option list
		// itself is now an allowlist vetted against FFmpeg's own help.
		{"x265 two-pass stats file", nil, []string{"-x265-stats", "stats.log"}, "-x265-stats"},
		{"lavfi graph from a file", []string{"-graph_file", "graph.txt"}, nil, "-graph_file"},
		{"lavfi graph dump", []string{"-dumpgraph", "1"}, nil, "-dumpgraph"},
		{"svtav1 stats key", nil, []string{"-svtav1-params", "stats=svt.stat"}, "stats="},
		{"x264 pass and stats keys", nil, []string{"-x264-params", "pass=2:stats=x264.log"}, "pass="},
		{"a TLS key file read", []string{"-key_file", "key.pem"}, nil, "-key_file"},
		// The third review: device options open an arbitrary path. Device
		// selection belongs to the server's encoder config, not expert args.
		{"vaapi device pointed at a file", []string{"-vaapi_device", "/etc/passwd"}, nil, "-vaapi_device"},
		{"init_hw_device with a path", []string{"-init_hw_device", "vaapi=x:/tmp/foo"}, nil, "-init_hw_device"},
		{"filter_hw_device", nil, []string{"-filter_hw_device", "x"}, "-filter_hw_device"},
		{"hwaccel_device", []string{"-hwaccel_device", "/dev/sda"}, nil, "-hwaccel_device"},
		{"escaping does not hide a filter", nil, []string{"-vf", `m\ovie=clip.mp4`}, "movie"},
		{"a file-writing muxer", nil, []string{"-f", "segment"}, "-f segment"},
		{"tee writes beside the URL", nil, []string{"-f", "tee"}, "-f tee"},
		{"hls segment path", nil, []string{"-hls_segment_filename", "/tmp/s%d.ts"}, "-hls_segment_filename"},
		{"x264 dump-yuv writes raw video", nil, []string{"-x264-params", "keyint=60:dump-yuv=/tmp/y"}, "dump-yuv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkExpertArgs(tc.in, tc.out, row)
			if err == nil {
				t.Fatalf("accepted in=%q out=%q, which touches a file on the server", tc.in, tc.out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused with %q, which does not name %q", err, tc.want)
			}
		})
	}
}

// The walk must not refuse ordinary stream settings, values that look like
// paths, or the booleans and their -no negations.
func TestExpertFileWalkAcceptsOrdinaryArguments(t *testing.T) {
	row := &db.Destination{ID: 1, Kind: db.DestRTMP}
	for _, tc := range []struct {
		name    string
		in, out []string
	}{
		{"muxer flags", nil, []string{"-muxdelay", "0.1", "-flvflags", "no_duration_filesize"}},
		{"booleans", []string{"-re", "-nostdin"}, []string{"-shortest", "-an"}},
		{"a value that is a path", nil, []string{"-metadata", "comment=see /docs"}},
		{"a negative value", []string{"-itsoffset", "-1.5"}, nil},
		{"an ordinary filter", nil, []string{"-af", "volume=0.8,aresample=48000"}},
		{"an ordinary muxer", nil, []string{"-f", "flv"}},
		{"ordinary x264 params", nil, []string{"-x264-params", "keyint=60:min-keyint=60"}},
		{"encoder settings", nil, []string{"-preset", "veryfast", "-tune", "zerolatency", "-g", "60", "-force_key_frames", "expr:gte(t,n_forced*2)"}},
		{"documented examples", []string{"-analyzeduration", "10M", "-probesize", "5M"}, []string{"-metadata", "title=Rock & Roll", "-muxdelay", "0", "-flvflags", "no_duration_filesize"}},
		{"srt options", nil, []string{"-latency", "200000", "-pbkeylen", "16"}},
		{"nvenc as docs/HARDWARE.md spells it", nil, []string{"-preset", "p1", "-rc", "cbr"}},
		{"vaapi upload filters", nil, []string{"-vf", "format=nv12,hwupload"}},
		{"a muxer flag", nil, []string{"-movflags", "+faststart"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := checkExpertArgs(tc.in, tc.out, row); err != nil {
				t.Errorf("refused in=%q out=%q: %v", tc.in, tc.out, err)
			}
		})
	}
}

// The route table is the other half: an API token -- admin scope included --
// cannot reach any expert route, so the argv can only be saved by a signed-in
// operator. The session still can.
func TestAnAPITokenCannotReachTheExpertRoutes(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	dest := createDestination(t, h, sign, destinationBody("expert-session", false, nil))
	path := "/api/v1/destinations/" + itoa(dest.ID) + "/expert"
	admin := createToken(t, h, sign, "automation")

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, path, nil},
		{http.MethodPut, path, expertBody("", "-muxdelay 0.1", false, true)},
		{http.MethodDelete, path, nil},
		{http.MethodPost, path + "/preview", expertBody("", "-muxdelay 0.1", false, false)},
		{http.MethodPost, path + "/dry-run", expertBody("", "-muxdelay 0.1", false, false)},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := jsonRequest(t, tc.method, tc.path, tc.body)
			r.Header.Set("Authorization", "Bearer "+admin)
			if w := do(t, h, r); w.Code != http.StatusForbidden {
				t.Errorf("admin token got %d, want 403: %s", w.Code, w.Body.String())
			}
		})
	}

	// The signed-in operator still saves.
	send(t, h, sign, http.MethodPut, path, expertBody("", "-muxdelay 0.1", false, true), http.StatusOK)
}

// Arguments saved before the stricter rules -- a loudnorm filter, say -- stop
// applying at the next start (the engine runs the same walk at spawn). The
// editor must say so rather than show them as if they ran.
func TestTheEditorSaysStoredArgumentsThatNoLongerValidateDoNotRun(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	dest := createDestination(t, h, sign, destinationBody("stored-loudnorm", false, nil))
	row, err := store.GetDestination(dest.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.ExtraOutputArgs = "-af loudnorm=I=-16"
	if _, err := store.UpdateDestination(row); err != nil {
		t.Fatalf("storing pre-upgrade expert args: %v", err)
	}
	var resp expertResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/destinations/"+itoa(dest.ID)+"/expert", nil, http.StatusOK), &resp)
	if !strings.Contains(resp.Warning, "starts without them") || !strings.Contains(resp.Warning, "loudnorm") {
		t.Errorf("warning %q does not say the stored loudnorm is not applied", resp.Warning)
	}
	if resp.Args.OutputArgs != "-af loudnorm=I=-16" {
		t.Errorf("the stored text was hidden: %q", resp.Args.OutputArgs)
	}
}
