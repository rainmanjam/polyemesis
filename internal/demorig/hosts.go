// Package demorig is the part of the demo rig that must be unit-tested because a
// bug in it could publish to a real platform; the seed driver is //go:build ignore.
package demorig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// DestRow is one destination as the fixture or the server holds it.
type DestRow struct{ Name, URL, BackupURL string }

// CheckHosts returns nil only if every row's URL, and BackupURL when set, parses
// and has a hostname equal to one of allowed (case-folded). The error names every
// offending destination and the host found, never userinfo or a stream key.
//
// It fails closed: an empty allow-list, an unparseable URL, a URL with no host,
// a trailing dot or a port-less look-alike are all refused. Parse errors are
// never included in the message because net/url quotes the whole URL in them.
func CheckHosts(rows []DestRow, allowed []string) error {
	var allow []string
	for _, a := range allowed {
		if a != "" {
			allow = append(allow, a)
		}
	}
	if len(allow) == 0 {
		return errors.New("demo rig: the destination allow-list is empty; refusing everything")
	}
	var bad []string
	for _, r := range rows {
		if msg := hostProblem(r.URL, allow); msg != "" {
			bad = append(bad, fmt.Sprintf("%s: %s", r.Name, msg))
		}
		if r.BackupURL != "" {
			if msg := hostProblem(r.BackupURL, allow); msg != "" {
				bad = append(bad, fmt.Sprintf("%s (backup): %s", r.Name, msg))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("demo rig: destinations outside the allow-list: %s", strings.Join(bad, "; "))
}

// hostProblem returns "" when raw is acceptable, else a short kind of problem
// (or the offending hostname) that never contains userinfo, path or key.
//
// Shape is checked before the host: a string that names the receiver but is a
// list, a tee description, a file or http URL, or carries a query that could
// redirect (srt streamid/host parameters) is refused even though its hostname
// would pass.
func hostProblem(raw string, allow []string) string {
	if raw == "" {
		return "empty URL"
	}
	for _, c := range raw {
		if unicode.IsSpace(c) || unicode.IsControl(c) || strings.ContainsRune(`|,\`, c) {
			return "URL has forbidden characters"
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "URL does not parse"
	}
	switch strings.ToLower(u.Scheme) {
	case "rtmp", "rtmps", "srt":
	default:
		return "URL scheme is not rtmp, rtmps or srt"
	}
	host := u.Hostname()
	if host == "" {
		return "URL has no host"
	}
	allowedHost := false
	for _, a := range allow {
		if strings.EqualFold(host, a) {
			allowedHost = true
			break
		}
	}
	if !allowedHost {
		return fmt.Sprintf("host %q is not allowed", host)
	}
	if strings.ContainsAny(raw, "?#") {
		return "URL has a query or fragment"
	}
	if strings.HasSuffix(u.Host, ":") {
		return "URL has an empty port"
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "URL port is not 1 to 65535"
		}
	}
	return ""
}
