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

// urlSecrets pulls the credential-bearing PARTS out of a URL rather than
// treating the whole thing as one.
//
// The whole URL is not a secret and masking it would cost the operator the host
// and the application, which is most of what a failing destination's log line is
// for. What is secret is the userinfo and, for the key-carrying schemes, the
// last path segment -- which is precisely what alerts.RedactURL already knows,
// except that RedactURL only fires when the text is recognisably a URL and the
// stderr path frequently mangles it past recognition.
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
	out = append(out, urlSecrets(pull.URL)...)
	return out
}
