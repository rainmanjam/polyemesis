package ffmpeg

import (
	"strings"
	"testing"
)

// TestPullURLCannotDialTheMetadataService is the finding: ValidatePullURL
// checked the SCHEME and nothing else, so the cloud metadata endpoint was a
// valid ingest source. internal/netguard -- the guard written so there would be
// exactly one address list -- was imported by alerts and hooks and by nothing
// that dials a pull.
func TestPullURLCannotDialTheMetadataService(t *testing.T) {
	refused := []struct {
		name string
		url  string
	}{
		{"the cloud metadata service", "http://169.254.169.254/latest/meta-data/iam/security-credentials/"},
		{"metadata over https", "https://169.254.169.254/computeMetadata/v1/"},
		{"any link-local address", "http://169.254.10.9/feed.ts"},
		{"loopback, which is polyemesis's own admin API", "http://127.0.0.1:8080/api/v1/settings"},
		{"loopback by another name", "http://127.7.7.7:9000/x.ts"},
		{"the unspecified address", "http://0.0.0.0:8080/x.ts"},
		{"IPv6 loopback", "http://[::1]:8080/x.ts"},
		{"IPv6 link-local", "http://[fe80::1]:8080/x.ts"},
		{"an IPv4-mapped loopback", "http://[::ffff:127.0.0.1]:8080/x.ts"},
		// The scheme is not the guard; every dialling family goes through it.
		{"rtsp to the metadata address", "rtsp://169.254.169.254/stream"},
		{"srt to loopback", "srt://127.0.0.1:9000"},
		{"rtmp to loopback", "rtmp://127.0.0.1:1935/live/key"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePullURL(tc.url)
			if err == nil {
				t.Fatalf("ValidatePullURL(%q) = nil; the pull would be dialled", tc.url)
			}
			if !strings.Contains(err.Error(), "may not dial") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// THE COST, PINNED. The guard is netguard.IsHostLocalAddr and not
// IsPublicAddr, because the most common pull source in this product is a camera
// on the local network. A guard that refused it is a guard that gets removed.
func TestPullURLStillAcceptsTheOrdinarySources(t *testing.T) {
	allowed := []string{
		"rtsp://192.168.1.50/stream1",       // the RTSP camera this feature is for
		"rtsp://10.0.0.8:554/h264",          // RFC1918, other block
		"rtsp://172.16.4.4/cam",             // RFC1918, the block people forget
		"http://100.64.0.1/feed.ts",         // CGNAT / Tailscale, reachable on purpose
		"https://cdn.example.com/live.m3u8", // the public case
		"rtsp://cam.local/stream1",          // a NAME is not resolved here; see checkPullHost
		"srt://peer.example:9000",
		"file://uploads/bars.ts",
	}
	for _, u := range allowed {
		if err := ValidatePullURL(u); err != nil {
			t.Errorf("ValidatePullURL(%q) = %v; this is a source operators really use", u, err)
		}
	}
}

// The refusal has to reach the -i string too, not just the validator: a guard
// that only the settings form asks is a guard the engine can walk around.
func TestAHostLocalPullNeverBecomesAnInputArgument(t *testing.T) {
	spec := IngestSpec{
		Kind:     IngestPull,
		PullURL:  "http://169.254.169.254/latest/meta-data/",
		RelayURL: "udp://127.0.0.1:20000",
	}
	if src, err := spec.PullSource(); err == nil {
		t.Fatalf("PullSource = %q, want a refusal", src)
	}
	args := IngestArgs(spec)
	for _, a := range args {
		if strings.Contains(a, "169.254.169.254") {
			t.Fatalf("the metadata address reached the command line: %v", args)
		}
	}
}

// TestHTTPPullBoundsWhatTheDemuxerMayOpen is defence in depth and is labelled
// as such. The audit's local-file-read claim DID NOT REPRODUCE on FFmpeg 9.0.1
// -- an m3u8 whose only segment was file:///tmp/canary.ts was refused with no
// flag at all -- but the same measurement showed that adding `file` to the list
// opens and reads it, which means the refusal is FFmpeg's default rather than
// our decision. This pins it as ours. See httpPullProtocols.
func TestHTTPPullBoundsWhatTheDemuxerMayOpen(t *testing.T) {
	for _, u := range []string{"http://origin.example/live.m3u8", "https://origin.example/x.ts"} {
		args := IngestArgs(IngestSpec{Kind: IngestPull, PullURL: u, RelayURL: "udp://127.0.0.1:20000"})
		got, ok := argsAfter(args, "-protocol_whitelist")
		if !ok {
			t.Fatalf("%s: no -protocol_whitelist in %v", u, args)
		}
		for _, need := range []string{"http", "https", "tcp", "tls"} {
			if !strings.Contains(got, need) {
				t.Errorf("%s: whitelist %q omits %q, which the source is made of", u, got, need)
			}
		}
		// The whole point. "file" must not be in the list -- and not as a
		// substring of something else either, so split on commas.
		for _, p := range strings.Split(got, ",") {
			if p == "file" {
				t.Errorf("%s: whitelist %q permits file, which is the local-file read", u, got)
			}
		}
	}
}

// The flag is scoped to the HTTP family. -protocol_whitelist applies to the
// input AVFormatContext, so an HTTP list on an RTSP or SRT pull would refuse
// the protocol that source IS.
func TestTheHTTPWhitelistDoesNotLeakOntoOtherFamilies(t *testing.T) {
	for _, u := range []string{"rtsp://cam.local/s", "srt://peer.example:9000", "rtmp://peer.example/live/k"} {
		args := IngestArgs(IngestSpec{Kind: IngestPull, PullURL: u, RelayURL: "udp://127.0.0.1:20000"})
		if got, ok := argsAfter(args, "-protocol_whitelist"); ok {
			t.Errorf("%s carries -protocol_whitelist %q", u, got)
		}
	}
}
