package engine

import (
	"net/url"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The exact credentials that reach an FFmpeg command line, collected per
// process so supervisor.Process can remove them from everything it renders.
//
// This file is the answer to "which strings on this argv are secret", and it is
// deliberately the ONLY answer. The alternative -- a matcher that reads the argv
// and decides what looks secret -- is what shipped, and it is grammatically
// incapable: FFmpeg's `-flag value` namespace is open, so `-rtmp_conn S:<key>`
// and `-passphrase <key>` are unrecognisable to any finite table of flag names.
// Here the question does not arise, because the value was ours before it was an
// argument.

// destSecrets is every credential a destination's command line can carry.
//
// Both feeds get the same set. The backup argv is built from the same row and
// splices the same expert text, so a set that covered only the primary would
// leave dest:N:backup leaking on exactly the routes dest:N no longer does.
func destSecrets(row *db.Destination) []string {
	if row == nil {
		return nil
	}
	out := []string{row.StreamKey, row.BackupStreamKey}
	out = append(out, urlSecrets(row.URL)...)
	out = append(out, urlSecrets(row.BackupURL)...)
	out = append(out, expertArgsSecrets(row.ExtraInputArgs)...)
	out = append(out, expertArgsSecrets(row.ExtraOutputArgs)...)
	return out
}

// expertArgsSecrets treats every non-flag token of an operator's expert text as
// a credential.
//
// Blunt on purpose. This is free text an operator pastes, and the only two
// things known about it are that a token beginning with '-' is a flag and that
// anything else is a value FFmpeg will interpret. Deciding WHICH values look
// secret is the grammar that failed; the accepted cost is that a destination's
// own hostnames get masked in its command line alongside its keys, and an admin
// still reads the raw argv through GET /destinations/{id}/expert.
//
// A parse failure yields nothing rather than falling back to the raw string:
// expertArgv already refuses to splice text it could not split, so there is no
// argv carrying it and nothing to scrub. Returning the whole blob here would
// instead make every log line containing any fragment of it come back masked.
func expertArgsSecrets(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	argv, err := ffmpeg.SplitArgs(raw)
	if err != nil {
		return nil
	}
	return alerts.OpaqueArgvValues(argv)
}

// urlSecrets pulls the credential-bearing PARTS out of a DESTINATION URL rather
// than treating the whole thing as one.
//
// The whole URL is not a secret and masking it would cost the operator the host
// and the application, which is most of what a failing destination's log line is
// for. What is secret is the userinfo and, for the key-carrying schemes, the
// last path segment -- which is precisely what alerts.RedactURL already knows,
// except that RedactURL only fires when the text is recognisably a URL and the
// stderr path frequently mangles it past recognition.
//
// THIS RULE IS CORRECT ONLY FOR A PUBLISH URL, and only because a destination's
// real credential is declared separately: destSecrets already carries
// row.StreamKey and row.BackupStreamKey as exact literals, so what is left here
// is the belt beside those braces. It is NOT correct for a pull URL, where the
// credential is in the URL and nowhere else -- see pullURLSecrets and #229.
func urlSecrets(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "://") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	var out []string
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			out = append(out, pw)
		}
		out = append(out, u.User.Username())
	}
	for k, vs := range u.Query() {
		if alerts.SecretName(k) {
			out = append(out, vs...)
		}
	}
	if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) > 1 {
		out = append(out, parts[len(parts)-1])
	}
	return out
}

// pullURLSecrets is urlSecrets for a PULL URL, where everything below the
// authority is a credential until proven otherwise.
//
// #229. urlSecrets' rule -- userinfo, the LAST path segment, and the query
// parameters alerts.SecretName recognises -- is a rule about where a credential
// lives in an RTMP PUBLISH URL: host + application + key, key last. A pull URL
// is not that shape. An RTSP camera or an Akamai/Wowza-style CDN puts the
// credential in the MIDDLE of the path and the FILENAME last, and names its
// query token whatever that CDN chose -- authcode, hdnts, policy -- none of
// which are in alerts.secretParam's twenty-name table, which is an exact lookup
// with no fallback.
//
// MEASURED on main at cffd20c, rendered exactly as GET /api/v1/processes
// renders it (Spec.Secrets scrubbed per argument, then alerts.Redact over the
// joined line):
//
//	in : rtsp://cam.example/live/SUPERSECRETPATHSEG/stream1
//	out: ffmpeg -i rtsp://cam.example/live/SUPERSECRETPATHSEG/[redacted]
//
//	in : https://cdn.example/live/index.m3u8?hdnts=HDNTSVALUE
//	out: ffmpeg -i https://cdn.example/live/[redacted]?hdnts=HDNTSVALUE
//
// In both the credential is printed in full AND something harmless is masked in
// its place, so the line LOOKS redacted. That is what let it survive three
// readings; it is the third instance of this class after #150 and #162.
//
// THE LITERAL IS THE WHOLE SUBSTRING BELOW THE AUTHORITY, not the segments and
// values one at a time, and that is the load-bearing part rather than a
// stylistic choice. alerts.NewSecretSet REFUSES anything shorter than
// alerts.MinSecretLen (8), so a set built segment-by-segment drops a
// six-character path segment on the floor and the mask relocates to the
// neighbouring filename -- the leak, still wearing a "[redacted]" beside it.
// One long literal spanning the path and the query carries the short pieces
// inside it. MEASURED, by applying exactly that alternative and running the
// guard: the six-character segment came back
//
//	rtsp://cam.example/live/[redacted]/Q7wR2z/[redacted]?format=ts&hdnts=[redacted]
//
// -- three masks in the line and the credential still in the middle of it.
//
// THE SEGMENTS AND QUERY VALUES ARE NOT ALSO DECLARED, and that is a decision
// rather than an omission. They were, in the first draft, as a belt for the
// stderr path. Then the mutation that should have justified them -- put the
// query back under alerts.SecretName's twenty-name table, keeping the whole
// path literal -- was applied and the guard STILL PASSED, because the mask
// already written into the middle of the URL makes it unparseable and
// alerts.maskUnparseable blanks everything from the '?' on. Nothing this
// repository can execute distinguishes shipping them from not, and a fix no
// test can be made to fail for is not a fix. They also cost something real:
// `index.m3u8` is ten characters, so declaring it would mask that word out of
// every log line the process ever writes.
//
// The accepted cost is that GET /processes shows `rtsp://cam.example[redacted]`
// for a pull source instead of the path. That is the direction this codebase
// has already chosen twice -- see alerts.OpaqueArgvValues on expert argv text --
// and a pull URL's path is not what an operator reads that page for: the scheme
// and the host survive, which is what says which camera is refusing them, and
// an admin still reads the configured URL back through GET /api/v1/settings.
func pullURLSecrets(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "://") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	var out []string
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			out = append(out, pw)
		}
		out = append(out, u.User.Username())
	}
	if below := belowAuthority(raw); below != "" {
		out = append(out, below)
	}
	return out
}

// belowAuthority returns everything from the first '/', '?' or '#' after the
// authority to the end of the URL -- path, query and fragment together.
//
// Cut out of the RAW text rather than rebuilt from url.URL, and that is not
// fussiness. SecretSet.Scrub is a SUBSTRING replacement over the argv the
// kernel was handed, so the literal has to be the bytes that are actually
// there. u.EscapedPath() and u.Query().Encode() both RE-SPELL their input --
// Encode sorts the parameters alphabetically -- and a re-spelled literal
// matches nothing at all, which is a mask that silently does not happen.
func belowAuthority(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		return ""
	}
	rest := raw[i+3:]
	j := strings.IndexAny(rest, "/?#")
	if j < 0 {
		return ""
	}
	return rest[j:]
}

// ingestSecrets is every credential an ingest listener's command line carries.
// token is the per-source publish token, which is the RTMP address the child
// dials and is a credential in its own right.
//
// Takes the three blocks rather than a settings struct because the primary
// listener reads db.IngestSettings and the standby reads
// db.BackupIngestSettings, which are the same three fields under two names. One
// function over the fields is what stops the standby -- the feed that carries
// the show when the primary drops -- from being the one that kept leaking.
func ingestSecrets(srt db.SRTSettings, rtmp db.RTMPSettings, pull db.PullSettings, token string) []string {
	out := []string{srt.Passphrase, rtmp.StreamKey, token}
	out = append(out, pullURLSecrets(pull.URL)...)
	return out
}
