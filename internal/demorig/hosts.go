// Package demorig is the part of the demo rig that must be unit-tested because a
// bug in it could publish to a real platform; the seed driver is //go:build ignore.
package demorig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
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

// hostProblem returns "" when raw is acceptable, else a message with no userinfo.
func hostProblem(raw string, allow []string) string {
	if raw == "" {
		return "empty URL"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "URL does not parse"
	}
	host := u.Hostname()
	if host == "" {
		return "URL has no host"
	}
	for _, a := range allow {
		if strings.EqualFold(host, a) {
			return ""
		}
	}
	return fmt.Sprintf("host %q is not allowed", host)
}
