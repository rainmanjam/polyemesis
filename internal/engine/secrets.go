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
// THREE expansions, and the second is the one that carries the measurement:
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
//   - THE PREFIX ENDING AT THE FIRST '?', which is the same defect reached by a
//     route nobody had to paste anything to hit. It is the SAME TRUNCATION
//     CLASS as the control-character case: a value is stored in one spelling
//     and appears on the wire in a shorter one, so the long literal cannot
//     match the short text.
//
//     THE CASE THAT SHIPPED. engine/multitrack.go registers the Twitch minted
//     key AFTER multitrack.withConfigID has appended "?clientConfigId=<uuid>"
//     -- Outcome.Target.Key is that composed value and there is no field
//     carrying the bare one. So the registered literal is strictly longer than
//     the minted key itself, and any text carrying the key WITHOUT the query
//     went unmasked: a manifest, an FFmpeg message that stopped at the '?',
//     anything that split the URL on its query. What survived was
//     v1_<signature>_<manifest>_<MASK> -- the operator's original key masked
//     because it is a suffix, and the signature and manifest standing. Exactly
//     the half-fix destSecrets's own doc comment above warns about, arrived at
//     from the other direction.
//
//     It is also the RIGHT spelling for an ordinary stream key, independently
//     of the minted one: Twitch keys carry documented query parameters
//     ("?bandwidthtest=true"), so a key stored with one has a bare form that
//     reaches the wire whenever the parameter is dropped.
//
//     The cost is bounded and known. wireSpellings is applied to literals that
//     are ALREADY secret in full -- keys, userinfo, last path segments, expert
//     argv values -- so this can only ever mask a PREFIX of something already
//     masked. The one place it widens reach is an expert-args token that is a
//     whole URL with a query, whose host would then be masked on its own too;
//     expertArgsSecrets is documented as blunt on purpose and already accepts
//     that a destination's own hostnames get masked beside its keys.
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
		// i > 0, so a value that IS a query -- one starting with '?' -- yields
		// nothing rather than the empty string. An empty literal in a SecretSet
		// would match at every position of every line.
		if i := strings.IndexByte(v, '?'); i > 0 {
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

// DestinationSecrets collects every credential literal carried by these rows,
// in every spelling each can wear on the wire.
//
// EXPORTED FOR THE DEBUG RECORDER, which needs the declared secrets and cannot
// reach destSecrets. internal/diag's scrubbing is only as good as the set it is
// given: alerts.Redact is a residual pass over shapes, and the exact-literal
// masking that actually removes a stream key needs the literal.
//
// The rows are read, never mutated. Callers pass whatever ListDestinations
// returned; a nil row is skipped rather than panicking, because the caller is a
// diagnostic path and must not be able to take the process down.
func DestinationSecrets(rows []*db.Destination) []string {
	var out []string
	for _, row := range rows {
		if row == nil {
			continue
		}
		out = append(out, destSecrets(row)...)
	}
	return out
}

// SourceSecrets collects every credential literal a source carries.
//
// EXPORTED FOR THE DEBUG RECORDER, AND ITS ABSENCE WAS A REAL LEAK. The
// recorder's set was first built from DestinationSecrets alone, which covers
// where a stream goes and nothing about where it comes from. A pull source is
// addressed by a URL that routinely carries credentials -- rtsp://user:pass@,
// or a CDN token in the query -- and engine.go logs that URL. Everything in it
// therefore reached the exported bundle with only the residual alerts.Redact
// pass behind it, which matches shapes rather than literals.
//
// The publish TOKEN is collected for the same reason: it is what an encoder
// authenticates with, it appears in the ingest URL an operator copies, and a
// token in a bundle sent to a stranger is a stranger who can publish.
//
// PrevToken travels too. Rotation keeps the old one working for five minutes,
// so during that window it is a live credential that log lines may still carry.
// SettingsSecrets collects the install-wide ingest credentials.
//
// THE FOURTH INVENTORY. Sources carry a per-programme ingest; Settings carries
// the install's own, AND the failover BACKUP ingest, and neither was ever
// handed to the debug recorder. internal/engine/selector.go logs the backup
// pull URL in full when it switches to it -- which is the moment an operator is
// most likely to be recording, because switching to backup is the fault they
// are trying to capture.
//
// Both blocks go through ingestSecrets, so the pull URL is read with
// pullURLSecrets rather than the publish-URL rule. That distinction is the one
// SourceSecrets got wrong (#229).
func SettingsSecrets(s db.Settings) []string {
	out := ingestSecrets(s.Ingest.SRT, s.Ingest.RTMP, s.Ingest.Pull, "")
	b := s.Failover.Backup
	out = append(out, ingestSecrets(b.SRT, b.RTMP, b.Pull, "")...)
	return wireSpellings(out)
}

// AccountSecrets collects every credential a connected platform account holds.
//
// THE THIRD INVENTORY, AND THE ONE THAT WAS MISSING. The debug recorder's
// SecretSet was built from destinations, then destinations plus sources after a
// review found pull-source credentials reaching the export. Platform accounts
// were never added, and they hold the OAuth access and refresh tokens.
//
// That gap is reachable rather than theoretical: a failed token refresh is
// preserved as a 300-character snippet of the provider's own response body
// (oauth.tokenStatusError) and logged at internal/api/oauth_handlers.go. A
// provider -- or a proxy in front of one -- that echoes the token in an error
// body puts that literal in the ring, where the declared set has never heard of
// it and alerts.Redact does not recognise arbitrary token shapes.
func AccountSecrets(rows []db.PlatformAccount) []string {
	var out []string
	for _, a := range rows {
		out = append(out, a.AccessToken, a.RefreshToken)
	}
	return wireSpellings(out)
}

func SourceSecrets(rows []*db.Source) []string {
	var out []string
	for _, s := range rows {
		if s == nil {
			continue
		}
		// DELEGATED TO ingestSecrets RATHER THAN REBUILT, and rebuilding it was
		// the bug. This called urlSecrets on the pull URL, and urlSecrets says in
		// its own comment that it "is NOT correct for a pull URL, where the
		// credential is in the URL and nowhere else -- see pullURLSecrets and
		// #229." It takes the LAST path segment, which for a publish URL is the
		// stream key and for a pull URL is the filename: a CDN URL ending
		// /SUPERSECRETPATHSEG/stream1/index.m3u8 was declared to the debug
		// recorder as "index.m3u8", so the credential left the box in the clear.
		//
		// The second copy also silently dropped rtmp.StreamKey, which
		// ingestSecrets has always carried. Two extractors for one concept is how
		// they drift; there is now one.
		out = append(out, s.Token, s.PrevToken)
		out = append(out, ingestSecrets(s.Ingest.SRT, s.Ingest.RTMP, s.Ingest.Pull, s.Token)...)
	}
	return wireSpellings(out)
}
