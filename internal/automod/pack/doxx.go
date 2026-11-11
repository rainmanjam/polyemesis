package pack

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode"
)

// Doxxing detectors. They run on the NFKC form, never the pack form: digits
// must stay digits ("l" is not "1" here), and a leet fold would turn a phone
// number into a word.
//
// A Hit names the DETECTOR only ("phone number"), never the matched value.
// recordAutomod logs Reason, and the value would copy the doxx into the log.

// ReservedPolicy decides which address ranges are never treated as a leaked
// address because they cannot identify a person's connection.
//
// Private, loopback, link-local and multicast ranges are always reserved. The
// documentation ranges (RFC 5737, RFC 3849) are reserved too, so that a chat
// message quoting one, which is harmless, is not reported. The fixture corpus
// uses exactly those ranges, because it may not contain real addresses, so the
// harness opts in to AllowDocumentation and runs the same detector rather than
// the detector having a test-only branch.
//
// THE ZERO VALUE IS THE PRODUCTION POLICY. A caller that builds a Config and
// says nothing about this rejects documentation ranges; only a caller that
// names the field accepts them.
type ReservedPolicy struct {
	// AllowDocumentation counts 192.0.2/24, 198.51.100/24, 203.0.113/24 and
	// 2001:db8::/32 as ordinary addresses. For the corpus harness only.
	AllowDocumentation bool
}

// DefaultReservedPolicy is what production runs. It is the zero value, named so
// a call site reads as a decision.
func DefaultReservedPolicy() ReservedPolicy { return ReservedPolicy{} }

var (
	docPrefixes = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	otherReserved = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
)

// publicServices are well-known public resolver addresses (Google, Cloudflare,
// Quad9, OpenDNS). Tech and networking chat quotes them constantly ("set your
// dns to 1.1.1.1"), and they identify a service, never a person's connection,
// so they are rejected the way a private range is. A short fixed list: it does
// not try to know every service address.
var publicServices = map[netip.Addr]bool{}

func init() {
	for _, s := range []string{
		"8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1", "9.9.9.9", "149.112.112.112",
		"208.67.222.222", "208.67.220.220",
		"2001:4860:4860::8888", "2001:4860:4860::8844",
		"2606:4700:4700::1111", "2606:4700:4700::1001", "2620:fe::fe", "2620:fe::9",
	} {
		publicServices[netip.MustParseAddr(s)] = true
	}
}

func (r ReservedPolicy) reserved(a netip.Addr) bool {
	a = a.Unmap()
	if publicServices[a] {
		return true
	}
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range otherReserved {
		if p.Contains(a) {
			return true
		}
	}
	if !r.AllowDocumentation {
		for _, p := range docPrefixes {
			if p.Contains(a) {
				return true
			}
		}
	}
	return false
}

// ------------------------------------------------------------------ phone

// phoneRuns finds candidate numbers: a plus or a digit (optionally behind an
// opening bracket) followed by digit groups joined by single spaces, dots or
// dashes, with "(ddd)" brackets allowed. A run may not start or end against a
// letter or another digit.
func phoneRuns(s string) []phoneRun {
	var out []phoneRun
	rs := []rune(s)
	n := len(rs)
	for i := 0; i < n; {
		if !startsCandidate(rs[i]) {
			i++
			continue
		}
		// A candidate may not start inside a word or a dotted/dashed chain
		// ("v10.0.19045.5000", "build-2024-123-4567"): consume the chain.
		if insideChain(rs, i) {
			i = consumeChain(rs, i)
			continue
		}
		if bareOpener(rs, i) {
			i++
			continue
		}
		i = recordRun(&out, rs, startPhoneRun(rs, i), i)
	}
	return out
}

// startsCandidate reports whether r can begin a phone number candidate: a digit,
// a plus, or an opening bracket.
func startsCandidate(r rune) bool {
	return unicode.IsDigit(r) || r == '+' || r == '('
}

// startPhoneRun scans the candidate that begins at i, including its leading plus
// or bracket and any separator after it.
func startPhoneRun(rs []rune, i int) phoneRun {
	r := rs[i]
	run := phoneRun{start: i, plus: r == '+'}
	j := i
	if r == '+' || r == '(' {
		j++
		if isPhoneSep(rs[j]) {
			j++
		}
	}
	run.end = scanPhoneRun(rs, &run, j)
	return run
}

// recordRun keeps run when it is a real candidate, and returns the index the
// scan resumes from. It is the caller's i that a run at i must advance past.
func recordRun(out *[]phoneRun, rs []rune, run phoneRun, i int) int {
	n := len(rs)
	if run.end < n && unicode.IsLetter(rs[run.end]) {
		return run.end
	}
	if run.digits > 0 {
		*out = append(*out, run)
	}
	if run.end <= i {
		run.end = i + 1
	}
	return run.end
}

// insideChain reports whether position i continues a word, or a dotted or
// dashed chain that starts before it.
func insideChain(rs []rune, i int) bool {
	return i > 0 && (isWordish(rs[i-1]) || isChainJoin(rs[i-1]) && i > 1 && isWordish(rs[i-2]))
}

// consumeChain returns the index just past the word or chain that begins at i.
func consumeChain(rs []rune, i int) int {
	n := len(rs)
	for i < n && (isWordish(rs[i]) || isChainJoin(rs[i]) && i+1 < n && isWordish(rs[i+1])) {
		i++
	}
	return i
}

// bareOpener reports a '(' or '+' at i that cannot begin a number: it is not
// followed by a digit, and a '+' is not followed by a separator and a digit.
func bareOpener(rs []rune, i int) bool {
	n := len(rs)
	r := rs[i]
	return r == '(' && !isDigAt(rs, i+1) || r == '+' && !isDigAt(rs, i+1) && !(i+2 < n && isPhoneSep(rs[i+1]) && isDigAt(rs, i+2))
}

// scanPhoneRun reads the digit groups, separators and brackets of a candidate
// from j on, filling run, and returns the index where the run stops.
func scanPhoneRun(rs []rune, run *phoneRun, j int) int {
	n := len(rs)
	grp := 0
	for j < n {
		c := rs[j]
		if unicode.IsDigit(c) {
			run.digits++
			grp++
			j++
			continue
		}
		if c == ')' && grp > 0 {
			j = closeBracket(rs, run, &grp, j)
			continue
		}
		if isPhoneSep(c) && grp > 0 && (isDigAt(rs, j+1) || j+2 < n && rs[j+1] == '(' && isDigAt(rs, j+2)) {
			j = closeSep(rs, run, &grp, j)
			continue
		}
		break
	}
	run.closeGroup(&grp)
	return j
}

// closeBracket takes the ')' at j, which closes a bracketed area code, and one
// space that a digit follows. It returns the index to resume from.
func closeBracket(rs []rune, run *phoneRun, grp *int, j int) int {
	run.closeGroup(grp)
	run.seps++
	j++
	if j < len(rs) && rs[j] == ' ' && isDigAt(rs, j+1) {
		j++
	}
	return j
}

// closeSep takes the separator at j, which ends a group, and one '(' that opens
// the next group. It returns the index to resume from.
func closeSep(rs []rune, run *phoneRun, grp *int, j int) int {
	run.closeGroup(grp)
	run.seps++
	j++
	if j < len(rs) && rs[j] == '(' {
		j++
	}
	return j
}

// closeGroup records the digit group just finished, if there is one, and starts
// the next.
func (p *phoneRun) closeGroup(grp *int) {
	if *grp > 0 {
		p.groups = append(p.groups, *grp)
		*grp = 0
	}
}

// isPhoneSep is a separator a phone number may place between its groups.
func isPhoneSep(r rune) bool { return r == ' ' || r == '.' || r == '-' || r == '/' }

// isDigAt reports whether rs has a digit at index i.
func isDigAt(rs []rune, i int) bool { return i >= 0 && i < len(rs) && unicode.IsDigit(rs[i]) }

func isWordish(r rune) bool   { return unicode.IsLetter(r) || unicode.IsDigit(r) }
func isChainJoin(r rune) bool { return r == '.' || r == '-' || r == '_' }

type phoneRun struct {
	start, end int
	plus       bool
	digits     int
	seps       int
	groups     []int
}

func (p phoneRun) text(rs []rune) string { return string(rs[p.start:p.end]) }

var (
	reDate  = regexp.MustCompile(`^\d{4}[-.]\d{2}[-.]\d{2}$`)
	reScore = regexp.MustCompile(`^\d{1,3}\s*[-]\s*\d{1,3}$`)
	// An IPv4-shaped run is an address (or a version), never a phone number.
	reDottedQuad = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
)

func monotonic(digits string) bool {
	same, up, down := true, true, true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			same = false
		}
		// Counting wraps: 1234567890 is the textbook fake number.
		if digits[i] != '0'+(digits[i-1]-'0'+1)%10 {
			up = false
		}
		if digits[i] != '0'+(digits[i-1]-'0'+9)%10 {
			down = false
		}
	}
	return same || up || down
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// hasPhone reports whether s holds a phone number. minSeps is how many
// separators a bare 10 or 11 digit run needs (2, or 3 for channels whose game
// posts scores).
func hasPhone(s string, minSeps int) bool {
	rs := []rune(s)
	for _, run := range phoneRuns(s) {
		t := run.text(rs)
		d := onlyDigits(t)
		if notPhoneRun(rs, run, t, d) {
			continue
		}
		if phoneNumberShape(run, d, minSeps) {
			return true
		}
	}
	return false
}

// notPhoneRun reports whether a candidate run is something else: an id, a
// dotted quad, a date or a score, a clock, or part of a URL.
func notPhoneRun(rs []rune, run phoneRun, t, d string) bool {
	if len(d) < 8 || monotonic(d) || reDottedQuad.MatchString(t) {
		return true
	}
	if reDate.MatchString(t) || reScore.MatchString(t) {
		return true
	}
	// A time inside the run ("12:30") is a clock, not a phone number; the
	// colon already ends the run, so look at what surrounds it.
	if run.start > 0 && rs[run.start-1] == ':' || run.end < len(rs) && rs[run.end] == ':' {
		return true
	}
	// A run inside a URL path or query is an id, not a phone number.
	return inURL(rs, run.start)
}

// phoneNumberShape reports whether a candidate run has the shape of a phone
// number: E.164 lengths for a plus, or a NANP shape for a bare 10 or 11 digits.
func phoneNumberShape(run phoneRun, d string, minSeps int) bool {
	switch {
	case run.plus:
		// E.164 lengths, any grouping.
		return len(d) >= 8 && len(d) <= 15
	case len(d) == 10 || (len(d) == 11 && d[0] == '1'):
		// NANP shape: 3-3-4 with a separator before the last group, or any
		// grouping with enough separators.
		return run.seps >= minSeps || nanpShape(run.groups, len(d)) && run.seps >= 1 && minSeps <= 2
	}
	return false
}

func nanpShape(groups []int, digits int) bool {
	g := groups
	if digits == 11 && len(g) > 0 && g[0] == 1 {
		g = g[1:]
	}
	return len(g) >= 2 && g[len(g)-1] == 4 && g[len(g)-2] == 3
}

// inURL reports whether position i of rs is inside a token that looks like a
// URL (scheme or www. prefix), by scanning back to the previous space.
func inURL(rs []rune, i int) bool {
	j := i
	for j > 0 && !unicode.IsSpace(rs[j-1]) {
		j--
	}
	tok := strings.ToLower(string(rs[j:i]))
	return strings.Contains(tok, "://") || strings.HasPrefix(tok, "www.")
}

// ------------------------------------------------------------------ email

var reEmailObf = regexp.MustCompile(`(?i)([a-z0-9][a-z0-9._%+\-]*)\s*[\[(]\s*at\s*[\])]\s*([a-z0-9.\-]+?)\s*[\[(]\s*dot\s*[\])]\s*([a-z]{2,})`)

func isLocalByte(c byte) bool {
	return isAlnumByte(c) || c == '.' || c == '_' || c == '%' || c == '+' || c == '-'
}

func isDomainByte(c byte) bool { return isAlnumByte(c) || c == '-' }

// emailDomains returns the domains of every address in s, including the
// "name [at] domain [dot] com" spelling. The plain form is scanned by hand: the
// regex it replaces was the most expensive thing the checker did on a line with
// an @-mention in it, which is most busy chat.
func emailDomains(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '@' {
			continue
		}
		if !hasLocalPart(s, i) {
			continue
		}
		if dom, ok := domainAt(s, i); ok {
			out = append(out, dom)
		}
	}
	return appendObfuscatedDomains(out, s)
}

// hasLocalPart reports whether the '@' at i has a local part to its left: one
// alphanumeric at least.
func hasLocalPart(s string, i int) bool {
	if i == 0 || !isLocalByte(s[i-1]) {
		return false
	}
	j := i - 1
	for j >= 0 && isLocalByte(s[j]) {
		j--
	}
	local := s[j+1 : i]
	return isAlnumByte(local[0]) || hasAlnum(local)
}

// domainAt reads the domain after the '@' at i and reports it when it is a
// real one. A domain to the right: labels of [a-z0-9-] joined by dots, ending in
// a label of two or more letters.
func domainAt(s string, i int) (string, bool) {
	k := i + 1
	for k < len(s) && (isDomainByte(s[k]) || s[k] == '.' && k+1 < len(s) && isDomainByte(s[k+1])) {
		k++
	}
	dom := strings.ToLower(s[i+1 : k])
	dot := strings.LastIndexByte(dom, '.')
	if dot < 1 || len(dom)-dot-1 < 2 {
		return "", false
	}
	tld := dom[dot+1:]
	for x := 0; x < len(tld); x++ {
		if tld[x] < 'a' || tld[x] > 'z' {
			return "", false
		}
	}
	return dom, true
}

// appendObfuscatedDomains adds the domains of "name [at] domain [dot] com"
// spellings to out, when the text has an [at] or (at) marker.
func appendObfuscatedDomains(out []string, s string) []string {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "[at]") || strings.Contains(lower, "(at)") ||
		strings.Contains(lower, "[ at ]") || strings.Contains(lower, "( at )") {
		for _, m := range reEmailObf.FindAllStringSubmatch(s, -1) {
			out = append(out, strings.ToLower(m[2]+"."+m[3]))
		}
	}
	return out
}

func hasAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		if isAlnumByte(s[i]) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------- IPv4

var reIPv4Cand = regexp.MustCompile(`\d{1,3}(?:\.\d{1,3}){3}`)

// hasIPv4 reports a public-looking IPv4 address: four dotted octets as a whole
// token, not part of a version string, and not in a reserved range.
func hasIPv4(s string, pol ReservedPolicy) bool {
	for _, loc := range reIPv4Cand.FindAllStringIndex(s, -1) {
		a, b := loc[0], loc[1]
		if !ipv4Bounded(s, a, b) {
			continue
		}
		if versionWord(s[:a]) {
			continue
		}
		addr, err := netip.ParseAddr(s[a:b])
		if err != nil {
			continue // an octet over 255, or a leading zero
		}
		if pol.reserved(addr) {
			continue
		}
		return true
	}
	return false
}

// ipv4Bounded reports whether the candidate s[a:b] stands alone: nothing that
// would make it part of a larger token sits on either side of it.
func ipv4Bounded(s string, a, b int) bool {
	// Preceded by a letter, digit, dot or dash: "v1.2.3.4", "jev-1.14.0.5".
	if a > 0 && ipv4JoinBefore(s[a-1]) {
		return false
	}
	return b >= len(s) || !ipv4JoinAfter(s, b)
}

// ipv4JoinBefore reports whether the byte before a candidate would make it part
// of a larger token.
func ipv4JoinBefore(p byte) bool {
	return p == '.' || p == '-' || p == '_' || isAlnumByte(p)
}

// ipv4JoinAfter reports whether the byte at b continues the candidate as one
// token: a letter, digit, dash or underscore, or a dot that a digit follows.
func ipv4JoinAfter(s string, b int) bool {
	n := s[b]
	if isAlnumByte(n) || n == '-' || n == '_' {
		return true
	}
	return n == '.' && b+1 < len(s) && s[b+1] >= '0' && s[b+1] <= '9'
}

// versionWords precede a dotted number that is a version, not an address.
var versionWords = map[string]bool{
	"version": true, "ver": true, "v": true, "build": true, "patch": true, "kernel": true,
	"release": true, "rev": true, "revision": true, "firmware": true, "fw": true,
	"update": true, "driver": true, "api": true, "sdk": true, "os": true,
}

// versionWord reports whether the word just before the end of prefix is one that
// introduces a version number.
func versionWord(prefix string) bool {
	p := strings.TrimRight(prefix, " :=#")
	i := len(p)
	for i > 0 && (p[i-1] >= 'a' && p[i-1] <= 'z' || p[i-1] >= 'A' && p[i-1] <= 'Z') {
		i--
	}
	return versionWords[strings.ToLower(p[i:])]
}

// ------------------------------------------------------------------- IPv6

var reV6Cand = regexp.MustCompile(`[0-9A-Fa-f:]{4,}(?:\.\d{1,3}){0,3}`)

// hasIPv6 reports an IPv6 address: at least five groups, or a "::" compression
// with at least three, and at least one group that has a hex letter or is longer
// than two digits (so 12:30:45 and 3:2:1 are clocks, not addresses).
func hasIPv6(s string, pol ReservedPolicy) bool {
	for _, loc := range reV6Cand.FindAllStringIndex(s, -1) {
		a, b := loc[0], loc[1]
		if a > 0 && isAlnumByte(s[a-1]) || b < len(s) && isAlnumByte(s[b]) {
			continue
		}
		tok := v6Token(s[a:b])
		if strings.Count(tok, ":") < 2 {
			continue
		}
		addr, err := netip.ParseAddr(tok)
		if err != nil || !addr.Is6() || addr.Is4In6() {
			continue
		}
		if !v6Shape(tok) {
			continue
		}
		if pol.reserved(addr) {
			continue
		}
		return true
	}
	return false
}

// v6Shape reports whether tok has the shape of an IPv6 address: enough groups
// (five, or three around a "::" compression), and at least one group that is
// strong (a hex letter, or longer than two digits).
func v6Shape(tok string) bool {
	groups, strong := v6Groups(tok)
	compressed := strings.Contains(tok, "::")
	return (groups >= 5 || compressed && groups >= 3) && strong
}

// v6Token restores the leading or trailing "::" that a candidate's trim removes,
// so netip can parse it.
func v6Token(raw string) string {
	tok := strings.Trim(raw, ":")
	if strings.HasPrefix(raw, "::") {
		tok = "::" + tok
	}
	if strings.HasSuffix(raw, "::") {
		tok += "::"
	}
	return tok
}

// v6Groups counts the non-empty groups of tok, and reports whether any group has
// a hex letter or is longer than two digits.
func v6Groups(tok string) (groups int, strong bool) {
	for _, g := range strings.Split(tok, ":") {
		if g == "" {
			continue
		}
		groups++
		if len(g) > 2 || strings.ContainsAny(g, "abcdefABCDEF") {
			strong = true
		}
	}
	return groups, strong
}

func isAlnumByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ----------------------------------------------------------------- address

var reAddress = regexp.MustCompile(`(?i)\b\d{1,5}\s+(?:[a-z.']+\s+){1,3}(?:st|street|ave|avenue|rd|road|blvd|boulevard|ln|lane|dr|drive|ct|court|way)\b\.?(?:\s+(?:apt|unit|suite|ste|#)\s*\w+)?`)

func hasAddress(s string) bool { return reAddress.MatchString(s) }
