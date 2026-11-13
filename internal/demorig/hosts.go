// Package demorig is the part of the demo rig that must be unit-tested because a
// bug in it could publish to a real platform; the seed driver is //go:build ignore.
package demorig

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
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

// CheckWebhookURLs is the question the driver asks of every alert-rule URL in
// the fixture before it is sent to the server: can it ever reach anybody? It
// returns nil only if each one is https, names a host under .invalid (which
// RFC 2606 reserves so it can never be delegated) and has no userinfo, port,
// query or fragment. A rule that fires then fails to resolve, on every install.
//
// Fails closed on an empty list's elements, a long URL or a character outside
// printable ASCII. The error names the rule, never the URL.
func CheckWebhookURLs(rules map[string]string) error {
	var bad []string
	for name, raw := range rules {
		if msg := webhookProblem(raw); msg != "" {
			bad = append(bad, fmt.Sprintf("%s: %s", name, msg))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
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
