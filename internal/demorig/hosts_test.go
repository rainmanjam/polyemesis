package demorig

import (
	"strings"
	"testing"
)

var recv = []string{"polyemesis-demo-recv"}

func row(name, url string) []DestRow { return []DestRow{{Name: name, URL: url}} }

func TestCheckHostsAcceptsTheReceiverOnly(t *testing.T) {
	for _, u := range []string{
		"rtmp://polyemesis-demo-recv:1935/live",
		"RTMP://POLYEMESIS-DEMO-RECV:1935/live",
		"rtmp://Polyemesis-Demo-Recv/live/key",
		"rtmp://polyemesis-demo-recv/polyemesis-demo-recv.evil.com",
	} {
		if err := CheckHosts(row("English", u), recv); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
	if err := CheckHosts(row("English", "rtmp://polyemesis-demo-recv/x"), []string{"Polyemesis-Demo-RECV"}); err != nil {
		t.Errorf("allow-list entry is case-folded too: %v", err)
	}
	if err := CheckHosts(nil, recv); err != nil {
		t.Errorf("no rows is fine: %v", err)
	}
}

func TestCheckHostsRefusesRealPlatformHosts(t *testing.T) {
	cases := []struct{ url, host string }{
		{"rtmp://a.rtmp.youtube.com/live2", "a.rtmp.youtube.com"},
		{"rtmps://live.twitch.tv/app", "live.twitch.tv"},
		{"rtmp://127.0.0.1:1935/live", "127.0.0.1"},
		{"rtmp://polyemesis-demo-recv.evil.com/live", "polyemesis-demo-recv.evil.com"},
		{"rtmp://polyemesis-demo-recv./live", "polyemesis-demo-recv."},
		{"rtmp://polyemesis-demo-recv@evil.com/live", ""},
		{"rtmp://evil.com/polyemesis-demo-recv", "evil.com"},
		{"rtmp://evil.com/live?h=polyemesis-demo-recv", "evil.com"},
		{"rtmp://[::1]:1935/live", "::1"},
		{"rtmp://polyemesis-demo-recv:1935@evil.com:1935/live", ""},
		{"rtmp://polyemesis-demo-recv%40evil.com/x", ""},
	}
	// Userinfo is refused outright: parsers disagree about which @ ends it.
	if err := CheckHosts(row("X", "rtmp://evil.com:1935@polyemesis-demo-recv/live"), recv); err == nil {
		t.Error("userinfo accepted")
	}
	for _, c := range cases {
		err := CheckHosts(row("Spanish", c.url), recv)
		if err == nil {
			t.Errorf("%q accepted", c.url)
			continue
		}
		if !strings.Contains(err.Error(), "Spanish") {
			t.Errorf("%q: error lacks the destination name: %v", c.url, err)
		}
		if c.host != "" && !strings.Contains(err.Error(), c.host) {
			t.Errorf("%q: error lacks the host %q: %v", c.url, c.host, err)
		}
	}
}

func TestCheckHostsRefusesABackupURL(t *testing.T) {
	err := CheckHosts([]DestRow{{Name: "English", URL: "rtmp://polyemesis-demo-recv/a", BackupURL: "rtmp://b.rtmp.youtube.com/live2"}}, recv)
	if err == nil || !strings.Contains(err.Error(), "English") || !strings.Contains(err.Error(), "b.rtmp.youtube.com") {
		t.Fatalf("backup url not refused by name and host: %v", err)
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("error should say it was the backup: %v", err)
	}
	if err := CheckHosts([]DestRow{{Name: "English", URL: "rtmp://polyemesis-demo-recv/a", BackupURL: "rtmp://polyemesis-demo-recv/b"}}, recv); err != nil {
		t.Errorf("allowed backup refused: %v", err)
	}
}

func TestCheckHostsRefusesEmptyAndUnparseable(t *testing.T) {
	for _, u := range []string{
		"", "::bad", "rtmp:///live", "rtmp://", "polyemesis-demo-recv", "polyemesis-demo-recv:1935/live",
		"/live", " rtmp://polyemesis-demo-recv/x", "rtmp://polyemesis-demo-recv /x", "rtmp://polyemesis-demo-recv\t/x",
		"rtmp://polyemesis-demo-recv\n.evil.com/x", "rtmp://%zz/x", "rtmp://polyemesis-demo-recv:notaport/x",
		"rtmp://[::1/x",
	} {
		err := CheckHosts(row("Arabic", u), recv)
		if err == nil {
			t.Errorf("%q accepted", u)
		} else if !strings.Contains(err.Error(), "Arabic") {
			t.Errorf("%q: error lacks name: %v", u, err)
		}
	}
	// An empty BackupURL means "not set", which is fine; whitespace-only is not.
	if err := CheckHosts([]DestRow{{Name: "E", URL: "rtmp://polyemesis-demo-recv/a", BackupURL: " "}}, recv); err == nil {
		t.Error("whitespace backup accepted")
	}
}

func TestCheckHostsReportsEveryOffender(t *testing.T) {
	err := CheckHosts([]DestRow{
		{Name: "One", URL: "rtmp://a.example.com/x"},
		{Name: "Ok", URL: "rtmp://polyemesis-demo-recv/x"},
		{Name: "Two", URL: ""},
		{Name: "Three", URL: "rtmp://polyemesis-demo-recv/x", BackupURL: "rtmp://c.example.org/x"},
	}, recv)
	if err == nil {
		t.Fatal("no error")
	}
	for _, w := range []string{"One", "Two", "Three", "a.example.com", "c.example.org"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %q: %v", w, err)
		}
	}
	if strings.Contains(err.Error(), "Ok") {
		t.Errorf("error names the good row: %v", err)
	}
}

func TestCheckHostsErrorNeverContainsAKey(t *testing.T) {
	for _, u := range []string{
		"rtmp://user:SECRETKEY@evil.com/x",
		"rtmp://user:SECRETKEY@evil.com/SECRETKEY",
		"rtmp://evil.com/live/SECRETKEY",
		"rtmp://user:SECRETKEY@[::1/x",
		"rtmp://user:SECRETKEY@%zz/x",
		"rtmp://user:SECRETKEY@/x",
		"SECRETKEY",
		" rtmp://user:SECRETKEY@evil.com/x",
		"rtmp://user:SECRETKEY@evil.com:bad/x",
	} {
		for _, rows := range [][]DestRow{
			{{Name: "n", URL: u}},
			{{Name: "n", URL: "rtmp://polyemesis-demo-recv/x", BackupURL: u}},
		} {
			err := CheckHosts(rows, recv)
			if err == nil {
				t.Errorf("%q accepted", u)
				continue
			}
			if strings.Contains(err.Error(), "SECRETKEY") || strings.Contains(err.Error(), "user:") {
				t.Errorf("%q leaked into the error: %v", u, err)
			}
		}
	}
}

func TestCheckHostsNeedsAnAllowList(t *testing.T) {
	for _, allowed := range [][]string{nil, {}, {""}} {
		if err := CheckHosts(row("E", "rtmp://polyemesis-demo-recv/x"), allowed); err == nil {
			t.Errorf("allow-list %q treated as allow-all", allowed)
		}
		if err := CheckHosts(nil, allowed); err == nil {
			t.Errorf("allow-list %q accepted with no rows", allowed)
		}
	}
	// An empty entry beside a real one must not make an empty host acceptable.
	if err := CheckHosts(row("E", "rtmp:///x"), []string{"", "polyemesis-demo-recv"}); err == nil {
		t.Error("empty entry matched an empty host")
	}
}

// Shape, not only host: a URL that names the receiver but is not a plain
// rtmp, rtmps or srt address to it must not pass.
func TestCheckHostsRefusesAnythingButAPlainURLToTheReceiver(t *testing.T) {
	r := "polyemesis-demo-recv"
	cases := []struct{ name, url, kind, mustNotLeak string }{
		{"file scheme", "file://" + r + "/etc/passwd", "scheme", "passwd"},
		{"http scheme", "http://" + r + "/x", "scheme", ""},
		{"https scheme", "https://" + r + "/x", "scheme", ""},
		{"scheme-relative", "//" + r + "/x", "scheme", ""},
		{"scheme-less with port", r + ":1935/live", "scheme", ""},
		{"trailing space", "rtmp://" + r + "/x ", "characters", ""},
		{"leading space", " rtmp://" + r + "/x", "characters", ""},
		{"trailing newline", "rtmp://" + r + "/x\n", "characters", ""},
		{"nul", "rtmp://" + r + "/x\x00", "characters", ""},
		{"tee string", "rtmp://" + r + "/live|[f=flv]rtmp://a.rtmp.youtube.com/k", "characters", "youtube"},
		{"comma list", "rtmp://" + r + "/a,rtmp://youtube/k", "characters", "youtube"},
		{"space list", "rtmp://" + r + "/a rtmp://youtube/k", "characters", "youtube"},
		{"backslash", "rtmp://" + r + "\\@evil.com/x", "characters", "evil"},
		{"srt query", "srt://" + r + ":1935?streamid=x&host=evil.com", "query", "evil"},
		{"rtmp query", "rtmp://" + r + "/live?x=1", "query", ""},
		{"empty query", "rtmp://" + r + "/live?", "query", ""},
		{"fragment", "rtmp://" + r + "/live#frag", "query", "frag-x"},
		{"fragment hiding a host", "rtmp://" + r + "#@evil/x", "userinfo", "evil"},
		{"port zero", "rtmp://" + r + ":0/x", "port", ""},
		{"port too big", "rtmp://" + r + ":65536/x", "port", ""},
		{"empty port", "rtmp://" + r + ":/x", "port", ""},
	}
	for _, c := range cases {
		err := CheckHosts(row("Latin", c.url), []string{r})
		if err == nil {
			t.Errorf("%s: %q accepted", c.name, c.url)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "Latin") || !strings.Contains(msg, c.kind) {
			t.Errorf("%s: error should name the destination and %q: %v", c.name, c.kind, err)
		}
		if c.mustNotLeak != "" && strings.Contains(msg, c.mustNotLeak) {
			t.Errorf("%s: error leaked part of the URL: %v", c.name, err)
		}
	}
	for _, u := range []string{
		"rtmp://" + r + "/live", "rtmps://" + r + ":443/live", "srt://" + r + ":6000", "SRT://" + r + ":65535",
		"RtMp://" + r + ":1/live/key",
	} {
		if err := CheckHosts(row("Latin", u), []string{r}); err != nil {
			t.Errorf("%q refused: %v", u, err)
		}
	}
	// The same shape rules apply to the backup URL.
	if err := CheckHosts([]DestRow{{Name: "L", URL: "rtmp://" + r + "/x", BackupURL: "http://" + r + "/y"}}, []string{r}); err == nil {
		t.Error("backup with a bad scheme accepted")
	}
}

func TestCheckHostsRefusesUserinfoNonASCIIAndHugeURLs(t *testing.T) {
	r := "polyemesis-demo-recv"
	cases := []struct{ name, url, kind, mustNotLeak string }{
		{"double at", "rtmp://" + r + "@evil.com@" + r + "/x", "userinfo", "evil"},
		{"user and password", "rtmp://u:p@" + r + "/x", "userinfo", "u:p"},
		{"zero-width space in path", "rtmp://" + r + "/li\u200bve", "characters", ""},
		{"non-ASCII letter in host", "rtmp://polyemesis-demo-r\u00e9cv/x", "characters", "cv"},
		{"del", "rtmp://" + r + "/x\x7f", "characters", ""},
		{"huge path", "rtmp://" + r + "/" + strings.Repeat("a", 100000), "too long", "aaaa"},
		{"one byte over", "rtmp://" + r + "/" + strings.Repeat("a", 2049-len("rtmp://"+r+"/")), "too long", "aaaa"},
	}
	for _, c := range cases {
		err := CheckHosts(row("Greek", c.url), []string{r})
		if err == nil {
			t.Errorf("%s accepted", c.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "Greek") || !strings.Contains(msg, c.kind) {
			t.Errorf("%s: error should name the destination and %q: %v", c.name, c.kind, err)
		}
		if c.mustNotLeak != "" && strings.Contains(msg, c.mustNotLeak) {
			t.Errorf("%s: error leaked the URL: %v", c.name, err)
		}
		if len(msg) > 300 {
			t.Errorf("%s: error is %d bytes long", c.name, len(msg))
		}
	}
	exact := "rtmp://" + r + "/" + strings.Repeat("a", 2048-len("rtmp://"+r+"/"))
	if err := CheckHosts(row("Greek", exact), []string{r}); err != nil {
		t.Errorf("a 2048-byte URL is the limit and should pass: %v", err)
	}
}

func TestCheckStoredRefusesWhatReachesAPlatformWithoutTheURL(t *testing.T) {
	r := "polyemesis-demo-recv"
	ok := DestRow{Name: "English", URL: "rtmp://" + r + "/x"}
	zero := int64(0)
	seven := int64(7)
	cases := []struct {
		name  string
		mod   func(d *DestRow)
		field string
	}{
		{"extra output args", func(d *DestRow) { d.ExtraOutputArgs = "-f flv rtmp://a.rtmp.youtube.com/k" }, "extraOutputArgs"},
		{"extra output args, redacted mask", func(d *DestRow) { d.ExtraOutputArgs = "[redacted]" }, "extraOutputArgs"},
		{"extra input args", func(d *DestRow) { d.ExtraInputArgs = "-re" }, "extraInputArgs"},
		{"extra input args, redacted mask", func(d *DestRow) { d.ExtraInputArgs = "***" }, "extraInputArgs"},
		{"account", func(d *DestRow) { d.AccountID = &seven }, "accountId"},
		{"account id zero", func(d *DestRow) { d.AccountID = &zero }, "accountId"},
		{"multitrack", func(d *DestRow) { d.Multitrack = true }, "multitrack"},
	}
	if err := CheckStored([]DestRow{ok}, []string{r}); err != nil {
		t.Fatalf("a plain row was refused: %v", err)
	}
	for _, c := range cases {
		d := ok
		c.mod(&d)
		err := CheckStored([]DestRow{d}, []string{r})
		if err == nil {
			t.Errorf("%s accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "English") || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: error should name the destination and %s: %v", c.name, c.field, err)
		}
		for _, leak := range []string{"youtube", "redacted", "***", "-re"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: error leaked field content %q: %v", c.name, leak, err)
			}
		}
	}
	// Host problems are still found, and every offender and field is reported.
	bad := DestRow{Name: "A", URL: "rtmp://evil.com/x"}
	both := DestRow{Name: "B", URL: "rtmp://" + r + "/x", Multitrack: true, AccountID: &seven}
	if err := CheckStored([]DestRow{bad, ok}, []string{r}); err == nil || !strings.Contains(err.Error(), "evil.com") {
		t.Errorf("host problem lost: %v", err)
	}
	err := CheckStored([]DestRow{both, {Name: "C", URL: ok.URL, ExtraInputArgs: "x"}}, []string{r})
	if err == nil {
		t.Fatal("no error")
	}
	for _, w := range []string{"B", "multitrack", "accountId", "C", "extraInputArgs"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %q: %v", w, err)
		}
	}
	// An empty allow-list is still refused through this entry point.
	if err := CheckStored([]DestRow{ok}, nil); err == nil {
		t.Error("empty allow-list accepted")
	}
}

func TestCheckWebhookURLsAcceptsInvalidHostsOnly(t *testing.T) {
	ok := map[string]string{
		"a": "https://alerts.demo.invalid/hooks/ops",
		"b": "HTTPS://Pager.Demo.INVALID/x/y",
	}
	if err := CheckWebhookURLs(ok); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if err := CheckWebhookURLs(nil); err != nil {
		t.Fatalf("no rules is fine: %v", err)
	}
}

func TestCheckWebhookURLsRefuses(t *testing.T) {
	for name, u := range map[string]string{
		"empty":      "",
		"real":       "https://example.com/hooks",
		"http":       "http://alerts.demo.invalid/x",
		"bare":       "https://invalid/x",
		"look-alike": "https://alerts.demo.invalid.evil.com/x",
		"userinfo":   "https://a@alerts.demo.invalid/x",
		"port":       "https://alerts.demo.invalid:8443/x",
		"emptyport":  "https://alerts.demo.invalid:/x",
		"query":      "https://alerts.demo.invalid/x?h=a.com",
		"space":      "https://alerts.demo.invalid/x y",
		"backslash":  `https://alerts.demo.invalid\@evil.com/`,
		"long":       "https://alerts.demo.invalid/" + strings.Repeat("a", 2100),
		"parse":      "https://alerts.demo.invalid/%zz",
		"ip":         "https://127.0.0.1/x",
	} {
		err := CheckWebhookURLs(map[string]string{name: u})
		if err == nil {
			t.Errorf("%s: %q accepted", name, u)
			continue
		}
		if u != "" && len(u) < 100 && strings.Contains(err.Error(), u) {
			t.Errorf("%s: the error quotes the URL: %v", name, err)
		}
	}
}
