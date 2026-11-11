package pack

import (
	"regexp"
	"strings"
	"unicode"
)

// Scam detectors (spec section 3.3). Phrase detectors run on the pack form
// (tokens separated by single spaces); link detectors run on the NFKC form,
// where a host's bytes are still what the sender typed.

// baitWords are the words of a giveaway or crypto lure. Any two within six
// tokens, plus a link or a handle, is a lure.
var baitWords = map[string]bool{
	"free": true, "giveaway": true, "airdrop": true, "claim": true, "double": true,
	"send": true, "wallet": true, "usdt": true, "btc": true, "eth": true,
	"nitro": true, "robux": true, "vbucks": true, "skins": true,
}

// baitPhrases are lures that need no link: the words alone are the ask.
var baitPhrases = []string{
	"send 1 get 2", "send 1 get 2 back", "free nitro", "claim your reward",
	"claim your prize", "free robux", "free vbucks", "free bitcoin",
}

var (
	reLinkOrAt = regexp.MustCompile(`(?i)https?://|www\.|(?:^|[^a-z0-9])[a-z0-9-]+\.(?:com|net|org|io|gg|tv|co|xyz|me|ly|to|app|gift)\b|@[a-z0-9_]{3,}`)
)

// shorteners are URL-shortening hosts. Versioned with the pack.
var shorteners = []string{
	"bit.ly", "tinyurl.com", "t.co", "cutt.ly", "is.gd", "rb.gy", "shorturl.at",
	"tiny.cc", "goo.su", "ow.ly", "buff.ly", "lnkd.in", "t.ly", "v.gd", "s.id",
	"bl.ink", "shorte.st", "clck.ru", "rebrand.ly", "tr.ee",
}

// brands are well-known hosts whose lookalikes are used in phishing.
var brands = []string{
	"twitch.tv", "youtube.com", "youtu.be", "discord.com", "discord.gg", "kick.com",
	"steampowered.com", "steamcommunity.com", "twitter.com", "x.com", "google.com",
	"paypal.com", "instagram.com", "facebook.com", "tiktok.com", "roblox.com",
	"epicgames.com", "binance.com", "coinbase.com", "amazon.com", "apple.com",
	"microsoft.com", "netflix.com", "reddit.com", "github.com",
}

// baitHit reports a giveaway or crypto lure in the pack form of a message.
func baitHit(f Forms) bool {
	padded := " " + f.Pack + " "
	for _, p := range baitPhrases {
		if strings.Contains(padded, " "+p+" ") {
			return true
		}
	}
	toks := f.Tokens
	last := -100
	count := 0
	for i, t := range toks {
		if !baitWords[t] {
			continue
		}
		if i-last > 6 {
			count = 0
		}
		count++
		last = i
		if count >= 2 {
			return reLinkOrAt.MatchString(f.NFKC)
		}
	}
	return false
}

// bioHit reports a check-my-bio pitch. Token matching rather than a regex: this
// runs on every message, and a plain chat line should cost nothing here.
func bioHit(f Forms) bool {
	t := f.Tokens
	for i := range t {
		if bioPitchAt(t, i) {
			return true
		}
	}
	return false
}

// bioPitchAt reports a check-my-bio pitch that starts at token i.
func bioPitchAt(t []string, i int) bool {
	switch t[i] {
	case "check":
		return checkMyBio(t, i)
	case "link":
		return linkInBio(t, i)
	case "dm":
		return dmLure(t, i)
	case "add":
		return addOnSnap(t, i)
	}
	return false
}

// checkMyBio matches "check my|out my bio|profile|channel|page".
func checkMyBio(t []string, i int) bool {
	j := i + 1
	if j < len(t) && t[j] == "out" {
		j++
	}
	return j+1 < len(t) && t[j] == "my" && pitchObject(t[j+1])
}

// linkInBio matches "link in (my) bio".
func linkInBio(t []string, i int) bool {
	if i+2 < len(t) && t[i+1] == "in" {
		if t[i+2] == "bio" || t[i+2] == "my" && i+3 < len(t) && t[i+3] == "bio" {
			return true
		}
	}
	return false
}

// dmLure matches "dm me for <something a lure offers>".
func dmLure(t []string, i int) bool {
	if !(i+3 < len(t) && t[i+1] == "me" && t[i+2] == "for") {
		return false
	}
	j := i + 3
	for j < len(t)-1 && (t[j] == "a" || t[j] == "the") {
		j++
	}
	if lureObject(t[j]) {
		return true
	}
	return (t[j] == "the" || t[j] == "a") && j+1 < len(t) && lureObject(t[j+1])
}

// addOnSnap matches "add me on snap..." or "add me on tele...".
func addOnSnap(t []string, i int) bool {
	return i+3 < len(t) && t[i+1] == "me" && t[i+2] == "on" &&
		(strings.HasPrefix(t[i+3], "snap") || strings.HasPrefix(t[i+3], "tele"))
}

func pitchObject(w string) bool {
	return w == "bio" || w == "profile" || w == "channel" || w == "page"
}

// lureObject is what "dm me for ..." offers when it is a lure; "dm me for ideas
// on the overlay design" is a streamer asking for help.
func lureObject(w string) bool {
	switch w {
	case "details", "info", "link", "promo", "collab", "collabs", "surprise", "free", "deal", "deals",
		"price", "prices", "rate", "rates", "cheap", "nudes", "pics", "content", "sponsor", "sponsors",
		"sponsorship", "crypto", "signals", "giveaway", "giveaways", "prize", "prizes", "reward",
		"rewards", "followers", "views":
		return true
	}
	return strings.HasPrefix(w, "invest") || strings.HasPrefix(w, "boost")
}

// supportHit reports fake platform support: an impersonated platform or role
// followed by an ask, or a ban threat.
func supportHit(f Forms) bool {
	t := f.Tokens
	for i := range t {
		if supportAt(t, i) {
			return true
		}
	}
	return false
}

// supportAt reports fake platform support that starts at token i.
func supportAt(t []string, i int) bool {
	switch t[i] {
	case "support", "team", "staff", "partner":
		return supportAsk(t, i)
	case "your":
		return bannedNotice(t, i)
	}
	return false
}

// supportAsk matches an impersonated platform or role at token i followed by an
// ask.
func supportAsk(t []string, i int) bool {
	if i == 0 || !supportWho(t[i-1]) {
		return false
	}
	for _, w := range t[i+1:] {
		if w == "dm" || w == "link" || w == "verify" || w == "verification" || w == "account" {
			return true
		}
	}
	return false
}

// bannedNotice matches "your channel|account will be|has been banned|suspended|flagged".
func bannedNotice(t []string, i int) bool {
	if !(i+4 < len(t) && (t[i+1] == "channel" || t[i+1] == "account")) {
		return false
	}
	j := i + 2
	if !(t[j] == "will" && t[j+1] == "be" || t[j] == "has" && t[j+1] == "been") {
		return false
	}
	switch t[j+2] {
	case "banned", "suspended", "flagged":
		return true
	}
	return false
}

func supportWho(w string) bool {
	switch w {
	case "official", "twitch", "youtube", "kick", "discord", "steam":
		return true
	}
	return false
}

// hostsOf returns the lower-cased host of every link-looking token in the NFKC
// text, in the sender's own bytes (no skeleton), without a leading "www.".
func hostsOf(nfkc string) []string {
	if !mayHoldHost(nfkc) {
		return nil
	}
	var out []string
	for _, tok := range strings.Fields(nfkc) {
		tok = strings.Trim(tok, "()<>[]{}\"'`*,;:!?")
		if tok == "" {
			continue
		}
		if i := strings.Index(tok, "://"); i >= 0 {
			tok = tok[i+3:]
		}
		// The host ends at a path, query, fragment or port.
		end := strings.IndexAny(tok, "/?#:")
		if end >= 0 {
			tok = tok[:end]
		}
		tok = strings.TrimSuffix(strings.ToLower(tok), ".")
		tok = strings.TrimPrefix(tok, "www.")
		if validHost(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// validHost reports a dotted name of letters, digits and hyphens (any script)
// whose last label is at least two letters.
func validHost(h string) bool {
	dot := strings.LastIndexByte(h, '.')
	if dot < 1 {
		return false
	}
	prevDot := true
	for _, r := range h {
		switch {
		case r == '.':
			if prevDot {
				return false
			}
			prevDot = true
			continue
		case r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r):
		default:
			return false
		}
		prevDot = false
	}
	if prevDot {
		return false
	}
	n := 0
	for _, r := range h[dot+1:] {
		if !unicode.IsLetter(r) {
			return false
		}
		n++
	}
	return n >= 2
}

// mayHoldHost is the cheap precheck for a link: a dot with a letter or digit on
// both sides, or a scheme. Most chat lines have neither, and the host regex is
// the expensive part of the scam detectors.
func mayHoldHost(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	for i := 1; i+1 < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		p, n := s[i-1], s[i+1]
		if (isAlnumByte(p) || p >= 0x80) && (isAlnumByte(n) || n >= 0x80) {
			return true
		}
	}
	return false
}

func hostIs(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// domainAllowed reports whether host is on the operator's allow-list.
func domainAllowed(host string, allow []string) bool {
	for _, d := range allow {
		if d != "" && hostIs(host, d) {
			return true
		}
	}
	return false
}

func shortenerHit(hosts, allow []string) bool {
	for _, h := range hosts {
		if domainAllowed(h, allow) {
			continue
		}
		for _, s := range shorteners {
			if hostIs(h, s) {
				return true
			}
		}
	}
	return false
}

// lookalikeHit reports a link host whose skeleton equals a well-known brand's
// but whose bytes differ.
func lookalikeHit(hosts, allow []string) bool {
	for _, h := range hosts {
		if domainAllowed(h, allow) {
			continue
		}
		sk := Skeleton(h)
		if sk == h {
			continue
		}
		for _, b := range brands {
			if hostIs(sk, b) && !hostIs(h, b) {
				return true
			}
		}
	}
	return false
}

// scamSignal reports whether the message carries any scam shape. Impersonation
// uses it as the second signal for its edit-distance arm.
func scamSignal(f Forms, hosts, allow []string) bool {
	return baitHit(f) || bioHit(f) || supportHit(f) || shortenerHit(hosts, allow) || lookalikeHit(hosts, allow)
}
