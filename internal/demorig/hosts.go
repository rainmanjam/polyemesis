// Package demorig is the part of the demo rig that must be unit-tested because a
// bug in it could publish to a real platform; the seed driver is //go:build ignore.
package demorig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// DestRow is one destination as the fixture or the server holds it. The JSON
// names are the server's, so the driver decodes stored rows straight into it.
//
// The last four fields are not about the URL: each makes a destination talk to
// a real platform (or run arbitrary FFmpeg output) without the URL saying so.
// A platform account's API is called at go-live, Enhanced Broadcasting
// negotiates with the platform, and extra arguments can name any output.
type DestRow struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Enabled         bool   `json:"enabled"`
	URL             string `json:"url"`
	BackupURL       string `json:"backupUrl"`
	ExtraInputArgs  string `json:"extraInputArgs"`
	ExtraOutputArgs string `json:"extraOutputArgs"`
	AccountID       *int64 `json:"accountId"`
	Multitrack      bool   `json:"multitrack"`
}

// maxURLBytes bounds the raw string: the rig's URLs are about 40 bytes.
const maxURLBytes = 2048

// CheckStored is the one question the driver asks of a stored row before it
// starts anything: is it safe? It is CheckHosts plus the four fields above,
// refused when set at all (a redacted mask is still "set"; an account id of 0
// is still an account). The error names each offending destination and field,
// never a value.
func CheckStored(rows []DestRow, allowed []string) error {
	if err := CheckHosts(rows, allowed); err != nil {
		return err
	}
	var bad []string
	for _, r := range rows {
		var fields []string
		if r.ExtraInputArgs != "" {
			fields = append(fields, "extraInputArgs")
		}
		if r.ExtraOutputArgs != "" {
			fields = append(fields, "extraOutputArgs")
		}
		if r.AccountID != nil {
			fields = append(fields, "accountId")
		}
		if r.Multitrack {
			fields = append(fields, "multitrack")
		}
		if len(fields) > 0 {
			bad = append(bad, fmt.Sprintf("%s: %s set", r.Name, strings.Join(fields, ", ")))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("demo rig: destinations that could reach a real platform without their URL: %s", strings.Join(bad, "; "))
}

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
	return urlProblem(raw, func(host string) string {
		for _, a := range allow {
			if strings.EqualFold(host, a) {
				return ""
			}
		}
		return fmt.Sprintf("host %q is not allowed", host)
	}, false)
}

// urlProblem is the shape rules shared by every URL check here. hostCheck
// returns "" for an acceptable hostname, else the problem. allowSRTQuery lets
// an srt URL carry the two harmless call options (see srtQueryOK); every other
// URL with a query or fragment is refused.
func urlProblem(raw string, hostCheck func(host string) string, allowSRTQuery bool) string {
	if raw == "" {
		return "empty URL"
	}
	if len(raw) > maxURLBytes {
		return "URL is too long"
	}
	// Printable ASCII only (0x21 to 0x7E): whitespace, controls and look-alike or
	// zero-width characters all fall outside it.
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x21 || c > 0x7e || c == '|' || c == ',' || c == '\\' {
			return "URL has forbidden characters"
		}
	}
	// No userinfo, by refusing any @: Go splits at the last one and other
	// clients at the first, so "a@evil@recv" has two different hosts.
	if strings.Contains(raw, "@") {
		return "URL has userinfo"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "URL does not parse"
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "rtmp", "rtmps", "srt":
	default:
		return "URL scheme is not rtmp, rtmps or srt"
	}
	host := u.Hostname()
	if host == "" {
		return "URL has no host"
	}
	if msg := hostCheck(host); msg != "" {
		return msg
	}
	if strings.Contains(u.Host, "%") {
		return "URL host is percent-encoded"
	}
	if strings.Contains(raw, "#") {
		return "URL has a query or fragment"
	}
	if strings.Contains(raw, "?") && !(allowSRTQuery && scheme == "srt" && srtQueryOK(u.RawQuery)) {
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

// srtQueryOK accepts only mode=(caller|listener|rendezvous) and latency=digits,
// each at most once: the options the full fixture's SRT row uses. A host=,
// streamid= or any other key could name another endpoint, so it is refused.
func srtQueryOK(q string) bool {
	seen := map[string]bool{}
	for _, kv := range strings.Split(q, "&") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return false
		}
		seen[k] = true
		switch k {
		case "mode":
			if v != "caller" && v != "listener" && v != "rendezvous" {
				return false
			}
		case "latency":
			if v == "" || len(v) > 7 {
				return false
			}
			for i := 0; i < len(v); i++ {
				if v[i] < '0' || v[i] > '9' {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// CheckInvalidDestinations is the full scene's first safety layer: the
// question asked of every destination in the full fixture before any container
// exists. It returns nil only if, for each row whose kind is not "file", the
// URL and (when set) BackupURL are rtmp, rtmps or srt URLs whose host is under
// .invalid (RFC 2606 reserves it, so it cannot resolve) with the shape rules
// CheckHosts applies, and the CheckStored refusals hold. An srt URL may carry
// mode and latency options only. A "file" row's URL and BackupURL have no
// scheme. The error names the destination, never a URL.
func CheckInvalidDestinations(rows []DestRow) error {
	var bad []string
	for _, r := range rows {
		var msgs []string
		if r.Kind != "file" && r.Enabled {
			msgs = append(msgs, "a network destination is enabled in the fixture (it would start the moment it is created)")
		}
		if r.Kind == "file" {
			if msg := fileProblem(r.URL, true); msg != "" {
				msgs = append(msgs, msg)
			}
			if msg := fileProblem(r.BackupURL, false); msg != "" {
				msgs = append(msgs, "backup: "+msg)
			}
		} else {
			if msg := invalidHostProblem(r.URL); msg != "" {
				msgs = append(msgs, msg)
			}
			if r.BackupURL != "" {
				if msg := invalidHostProblem(r.BackupURL); msg != "" {
					msgs = append(msgs, "backup: "+msg)
				}
			}
		}
		for _, m := range msgs {
			bad = append(bad, fmt.Sprintf("%s: %s", r.Name, m))
		}
	}
	var errs []string
	if len(bad) > 0 {
		errs = append(errs, "destinations that could reach a real host: "+strings.Join(bad, "; "))
	}
	// CheckStored with a throwaway allow-list: only its field refusals matter
	// here, so the host half is stripped by giving every row a passing URL.
	stripped := make([]DestRow, len(rows))
	for i, r := range rows {
		r.URL, r.BackupURL = "rtmp://x.invalid/x", ""
		stripped[i] = r
	}
	if err := CheckStored(stripped, []string{"x.invalid"}); err != nil {
		errs = append(errs, strings.TrimPrefix(err.Error(), "demo rig: "))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("demo rig: %s", strings.Join(errs, "; "))
}

func invalidHostProblem(raw string) string {
	return urlProblem(raw, func(host string) string {
		h := strings.ToLower(host)
		if !strings.HasSuffix(h, ".invalid") || len(h) == len(".invalid") || strings.HasPrefix(h, ".") {
			return "host is not under .invalid"
		}
		return ""
	}, true)
}

// fileProblem: a file destination is a name with no scheme. The primary URL
// must be set; the backup may be empty.
func fileProblem(raw string, primary bool) string {
	if raw == "" {
		if primary {
			return "empty file name"
		}
		return ""
	}
	if len(raw) > maxURLBytes {
		return "file name is too long"
	}
	if strings.Contains(raw, ":") || strings.Contains(raw, "//") {
		return "file name has a scheme"
	}
	return ""
}

// WebhookRule is one alert rule as the fixture holds it: the name is only for
// the error message, and two rules may share one.
type WebhookRule struct {
	Name string
	URL  string
}

// CheckWebhookURLs is the question the driver asks of every alert-rule URL in
// the fixture before it is sent to the server: can it ever reach anybody? It
// returns nil only if each one is https, names a host under .invalid (which
// RFC 2606 reserves so it can never be delegated) and has no userinfo, port,
// query or fragment. A rule that fires then fails to resolve, on every install.
//
// EVERY ROW IS CHECKED, in order: a slice, not a map keyed by name, because two
// rules with one name would otherwise hide the first one's URL from the check.
//
// Fails closed on an empty URL, a long URL or a character outside printable
// ASCII. The error names the rule, never the URL.
func CheckWebhookURLs(rules []WebhookRule) error {
	var bad []string
	for _, r := range rules {
		if msg := webhookProblem(r.URL); msg != "" {
			bad = append(bad, fmt.Sprintf("%s: %s", r.Name, msg))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("demo rig: alert URLs that could be reached: %s", strings.Join(bad, "; "))
}

func webhookProblem(raw string) string {
	if raw == "" {
		return "empty URL"
	}
	if len(raw) > maxURLBytes {
		return "URL is too long"
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x21 || c > 0x7e || c == '\\' {
			return "URL has forbidden characters"
		}
	}
	if strings.Contains(raw, "@") {
		return "URL has userinfo"
	}
	if strings.ContainsAny(raw, "?#") {
		return "URL has a query or fragment"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "URL does not parse"
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "URL scheme is not https"
	}
	if u.Port() != "" || strings.HasSuffix(u.Host, ":") {
		return "URL has a port"
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".invalid") || len(host) == len(".invalid") {
		return "host is not under .invalid"
	}
	return ""
}
