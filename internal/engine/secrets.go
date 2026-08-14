package engine

import (
	"net/url"
	"strings"
	"unicode"

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
//
// extra carries credentials that are NOT on the row because they did not exist
// until go-live. Today that is the Twitch Enhanced Broadcasting minted key, and
// it is a variadic rather than another row field because it is a fact about
// this run of this process, not about the destination -- a new one is minted
// per negotiation, and storing it would be storing a credential that is stale
// by the next broadcast.
//
// THE MINTED KEY NEEDS ITS OWN ENTRY AND CANNOT INHERIT THE ORIGINAL'S.
// SecretSet.Scrub is a substring replace, and the minted key ENDS WITH the
// operator's original -- v1_<signature>_<manifest>_<original>. Registering only
// the original therefore masks the last segment and leaves the signature and
// the manifest standing, which is a partially redacted credential in a log
// file: enough to identify the broadcast, and exactly the shape of half-fix
// that reads as protection. Measured and pinned by
// TestTheMintedKeyIsMaskedWholeAndNotJustItsTail.
func destSecrets(row *db.Destination, extra ...string) []string {
	if row == nil {
		return nil
	}
	out := []string{row.StreamKey, row.BackupStreamKey}
	out = append(out, extra...)
	out = append(out, urlSecrets(row.URL)...)
	out = append(out, urlSecrets(row.BackupURL)...)
	out = append(out, expertArgsSecrets(row.ExtraInputArgs)...)
	out = append(out, expertArgsSecrets(row.ExtraOutputArgs)...)
	return wireSpellings(out)
}

// wireSpellings expands each collected literal into the other spellings that
// value can wear by the time it reaches a command line or a log line.
//
// #306, and it is the same shape as userinfoSecrets below: SecretSet.Scrub
// finds the secret INSIDE the text, so a literal collected in one spelling
// masks nothing at all in text carrying another. userinfoSecrets learned that
// url.URL DECODES; this learned that a value can be TRUNCATED.
//
// MEASURED on a live run. A destination's key was configured with a
// bracketed-paste artefact glued on -- the real key followed by ESC [ 2 7 ; 2 ;
// 1 3 -- so the stored value was 65 bytes. FFmpeg stopped reading the publish
// URL at the ESC, opened the 56-byte prefix, failed, and printed that prefix
// back on stderr. Against the actual process.log:
//
//	raw 65-byte value in log : False
//	percent-encoded in log   : False
//	value-before-ESC in log  : True   (len 56)
//
// The scrub was wired, the destination did declare its Secrets, and the key
// still reached disk in the clear -- because the 65-byte needle does not occur
// inside the 56-byte haystack.
//
// db.Destination.Validate now REFUSES a key with a control character in it, and
// that is the fix; this is the defence in depth behind it. Part 1 only closes
// the causes somebody thought of, and the general defect -- the stored spelling
// and the wire spelling can diverge -- outlives its first instance. A row
// written by an older release, a field this check does not cover, a
// transformation nobody has hit yet: this pass costs one scan of a handful of
// short strings and holds for all of them.
//
// TWO expansions, and the second is the one that carries the measurement:
//
//   - THE TRIMMED FORM. alerts.NewSecretSet trims every value it is handed
//     today, so this is currently redundant THERE -- it is emitted because what
//     destSecrets promises its callers is "every spelling", not "every spelling
//     given what one particular consumer happens to do first".
//
//   - THE PREFIX ENDING AT THE FIRST CONTROL CHARACTER, which is the form the
//     wire actually carried. Trimming does not reach it: ESC is not whitespace,
//     so strings.TrimSpace returns the 65 bytes unchanged.
//
// ONLY THE FIRST PREFIX, and not every control-delimited segment, which the
// first draft emitted. The mechanism is TRUNCATION AT THE FIRST CONTROL BYTE:
// an artefact landing in FRONT of the material does not put the credential in a
// later segment that needs covering, it stops FFmpeg before the credential and
// the key never reaches the wire at all. Nothing in this repository can be made
// to fail for the later segments -- and the argument against shipping a literal
// no test can fail for is written out at length under pullURLSecrets below.
// They also cost something real: every extra literal is more text masked out of
// every log line this destination ever writes.
//
// Over-masking is the safe direction where it buys something, and that is the
// direction every other decision in this file went.
func wireSpellings(values []string) []string {
	out := make([]string, 0, len(values)*2)
	out = append(out, values...)
	for _, v := range values {
		if t := strings.TrimSpace(v); t != v && t != "" {
			out = append(out, t)
		}
		if i := strings.IndexFunc(v, unicode.IsControl); i > 0 {
			out = append(out, v[:i])
		}
	}
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
	out = append(out, userinfoSecrets(raw, u)...)
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

// userinfoSecrets is the username and password of a URL in BOTH the spelling
// url.URL gives them and the spelling the argv actually carries.
//
// #229's residual, and the reason it is a function rather than four lines
// inline: url.URL.User DECODES. On
//
//	rtsp://user:p%40ssw0rd%21@cam.example.com/stream/1
//
// u.User.Password() returns `p@ssw0rd!`, and SecretSet.Scrub is a SUBSTRING
// replacement over the argv the kernel was handed -- which contains
// `p%40ssw0rd%21`. The decoded literal matches nothing there, so the password
// was rendered verbatim into GET /api/v1/processes, a route a READ-SCOPED
// TOKEN CAN REACH. Every character that forces percent-encoding -- @ / ! # : %
// -- is ordinary in a generated credential, so this was not an exotic shape.
//
// Both spellings are emitted rather than only the raw one. They are identical
// whenever the password needs no encoding, and SecretSet already de-duplicates
// and drops anything under alerts.MinSecretLen; where they differ, the decoded
// form is what a log line built from url.URL rather than from argv would carry.
// Over-masking is the safe direction here and is the direction the ledger's own
// note on MinSecretLen argues for.
//
// The raw halves are cut out of the ORIGINAL TEXT for the same reason
// belowAuthority is: u.User.String() re-spells its input, and a literal that
// has been re-spelled is a literal that no longer matches the bytes on the
// command line.
func userinfoSecrets(raw string, u *url.URL) []string {
	var out []string
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			out = append(out, pw)
		}
		out = append(out, u.User.Username())
	}
	if user, pass := rawUserinfo(raw); user != "" || pass != "" {
		if pass != "" {
			out = append(out, pass)
		}
		if user != "" {
			out = append(out, user)
		}
	}
	return out
}

// rawUserinfo returns the userinfo exactly as it was typed: the bytes between
// "://" and the '@' that ends the authority, split at the first ':'.
//
// The authority is bounded at the first '/', '?' or '#' BEFORE the '@' is
// sought, so a path containing an '@' -- legal, and ordinary in S3-style URLs
// -- cannot be mistaken for a credential separator. LastIndex within that
// bound, because an '@' may appear inside the password itself.
func rawUserinfo(raw string) (user, pass string) {
	i := strings.Index(raw, "://")
	if i < 0 {
		return "", ""
	}
	authority := raw[i+3:]
	if j := strings.IndexAny(authority, "/?#"); j >= 0 {
		authority = authority[:j]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return "", ""
	}
	info := authority[:at]
	if c := strings.Index(info, ":"); c >= 0 {
		return info[:c], info[c+1:]
	}
	return info, ""
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
	out = append(out, userinfoSecrets(raw, u)...)
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
//
// wireSpellings applies here too, and for a sharper reason than on the
// destination side: srt.Passphrase and rtmp.StreamKey are operator-typed text
// with no parser between the form and the argv, and unlike a destination's key
// they have no db.Destination.Validate refusing a control character first. The
// pull URL cannot carry one -- url.Parse inside pullURLSecrets rejects it and
// returns nothing -- and the publish token is generated here, so for those two
// the pass is a no-op.
func ingestSecrets(srt db.SRTSettings, rtmp db.RTMPSettings, pull db.PullSettings, token string) []string {
	out := []string{srt.Passphrase, rtmp.StreamKey, token}
	out = append(out, pullURLSecrets(pull.URL)...)
	return wireSpellings(out)
}
