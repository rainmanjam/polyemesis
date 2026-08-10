package alerts

import (
	"log/slog"
	"sort"
	"strings"
)

// MinSecretLen is the shortest literal a SecretSet will hold.
//
// This is the one heuristic in a design whose entire selling point is
// exactness, and it is here on purpose rather than by oversight, so it is
// written down where a reviewer reads it rather than only in a code comment:
// see the reason text carried beside the on-disk route-coverage ledger.
//
// A literal shorter than this is REFUSED and LOGGED at construction. Two
// failure directions meet here and they are not symmetric:
//
//   - Too LOW and the set holds a short string that also occurs innocently in
//     FFmpeg's own output, so a diagnostic line comes back as "[redacted]" and
//     the operator loses the thing they opened the page for. The failure is
//     over-masking. Nothing escapes.
//   - Too HIGH and a genuinely short credential is not covered by the exact
//     pass at all. alerts.Redact still runs over the same bytes as the residual
//     outer pass, but Redact is best-effort and grammatically incapable over
//     FFmpeg's open `-flag value` namespace, so this IS a real, permanent
//     residual on the stderr path for very short credentials.
//
// 8 is chosen because every credential this system actually mints or accepts is
// longer: SRT refuses a passphrase under 10 characters, publish tokens are
// generated at 24, and no platform issues an RTMP stream key that short. An
// operator who pastes a 6-character secret into expert mode is outside what the
// exact pass covers, and that is stated rather than hidden.
const MinSecretLen = 8

// ffmpegCommonWord is the denylist: strings that are long enough to pass
// MinSecretLen but that occur in ordinary FFmpeg output often enough that
// accepting one would blind the log rather than clean it.
//
// A value on this list is REFUSED and LOGGED, exactly like a too-short one. It
// is short deliberately: it exists for the values an operator plausibly types
// into an expert-mode field that are not secrets, not for every FFmpeg token.
var ffmpegCommonWord = map[string]bool{
	"ultrafast": true, "superfast": true, "veryfast": true, "faster": true,
	"baseline": true, "yuv420p": true, "yuv422p": true, "yuv444p": true,
	"libx264": true, "libx265": true, "libfdk_aac": true, "aresample": true,
	"mpegts": true, "matroska": true, "flvflags": true, "no_duration_filesize": true,
	"experimental": true, "zerolatency": true, "stereo": true, "monaural": true,
	"copyts": true, "genpts": true, "nobuffer": true,
}

// SecretSet is a set of EXACT credential literals to remove from any text that
// is about to be shown, logged or published.
//
// Exact, not heuristic, and that is the whole point. alerts.Redact recognises
// URLs, `key=value` pairs and space-separated Bearer headers, and it was the
// only masking on the process-argv egresses. That is a GRAMMAR over an open
// namespace: FFmpeg accepts arbitrary `-flag value` pairs, so `-rtmp_conn
// S:<key>` and `-passphrase <key>` are invisible to it, and `Authorization:
// Bearer\ <key>` splits into argv entries where the word "Bearer" is masked and
// the token is handed back. No amount of added regexes closes that, because the
// set of flag spellings is not enumerable.
//
// A SecretSet inverts the question. Instead of asking "does this text look like
// it contains a credential", it asks "does this text contain one of the exact
// strings we KNOW are credentials, because we put them on the command line
// ourselves". That question has a correct answer, and it does not depend on how
// the credential was spelled into the argv.
//
// Redact remains as a residual outer pass for the credentials this set cannot
// know about: an endpoint that echoes a token back in an error string, a URL
// FFmpeg synthesised. It is not the boundary and must never be treated as one.
//
// The zero value is usable and scrubs nothing, so a Spec that declares no
// secrets needs no special-casing at the call sites.
type SecretSet struct {
	// lits is sorted LONGEST FIRST. A stream key is frequently a prefix or
	// suffix of the publish URL that carries it, and replacing the short one
	// first would leave the long one half-masked and still recognisable.
	lits []string
}

// NewSecretSet builds a set from the literals a process was configured with.
//
// Refusals are LOGGED, never silently dropped: a credential that does not make
// it into the set is a residual, and a residual nobody was told about is how
// this class of bug survives four review rounds. log may be nil, which is what
// the hashing and signature paths want.
func NewSecretSet(log *slog.Logger, values ...string) *SecretSet {
	seen := map[string]bool{}
	s := &SecretSet{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		if len(v) < MinSecretLen {
			if log != nil {
				log.Debug("secret literal refused: shorter than the exact-match floor, "+
					"so only the residual alerts.Redact pass covers it",
					"length", len(v), "floor", MinSecretLen)
			}
			continue
		}
		if ffmpegCommonWord[strings.ToLower(v)] {
			if log != nil {
				log.Debug("secret literal refused: it is ordinary FFmpeg vocabulary and "+
					"masking it would blind the log rather than clean it",
					"value", v)
			}
			continue
		}
		seen[v] = true
		s.lits = append(s.lits, v)
	}
	sort.Slice(s.lits, func(i, j int) bool {
		if len(s.lits[i]) != len(s.lits[j]) {
			return len(s.lits[i]) > len(s.lits[j])
		}
		return s.lits[i] < s.lits[j]
	})
	return s
}

// Len reports how many literals survived construction, so a guard can tell an
// empty set from a populated one and refuse to pass on a vacuous fixture.
func (s *SecretSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.lits)
}

// Scrub replaces every occurrence of every literal with Mask.
//
// Substring replacement rather than token matching, deliberately: the credential
// reaches the argv glued to whatever surrounded it -- `S:<key>`,
// `Authorization:Bearer\<key>`, `rtmps://host/app/<key>` -- and a token-boundary
// rule would miss all three. There is no shape of surrounding text that defeats
// this, which is exactly the property alerts.Redact cannot have.
func (s *SecretSet) Scrub(text string) string {
	if s == nil || len(s.lits) == 0 || text == "" {
		return text
	}
	for _, lit := range s.lits {
		text = strings.ReplaceAll(text, lit, Mask)
	}
	return text
}

// ScrubArgv applies Scrub to every element, returning a new slice. The input is
// never mutated: it is the argv the kernel was handed and expert mode reads it
// back to show an operator their own edit.
func (s *SecretSet) ScrubArgv(argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = s.Scrub(a)
	}
	return out
}

// OpaqueArgvValues returns every token in an argv that does not begin with '-'.
//
// This is how expert-mode text becomes a set of literals without anyone having
// to enumerate FFmpeg's flags. The rule is deliberately blunt: in `-flag value`
// the flag is recognisable and the value is not, so every value is treated as
// though it might be a credential. A destination's own hostnames and container
// names get masked in its command line along with its keys.
//
// That over-masking is the accepted cost and the correct direction. The
// alternative -- deciding WHICH values look secret -- is the grammar that
// already failed, and an admin still has the raw argv through GET
// /destinations/{id}/expert.
func OpaqueArgvValues(argv []string) []string {
	var out []string
	for _, a := range argv {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}
