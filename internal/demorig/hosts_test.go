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
		{"rtmp://polyemesis-demo-recv@evil.com/live", "evil.com"},
		{"rtmp://evil.com/polyemesis-demo-recv", "evil.com"},
		{"rtmp://evil.com/live?h=polyemesis-demo-recv", "evil.com"},
		{"rtmp://[::1]:1935/live", "::1"},
		{"rtmp://polyemesis-demo-recv:1935@evil.com:1935/live", "evil.com"},
		{"rtmp://polyemesis-demo-recv%40evil.com/x", ""},
	}
	// Userinfo naming another host does not change the hostname: this is the receiver.
	if err := CheckHosts(row("X", "rtmp://evil.com:1935@polyemesis-demo-recv/live"), recv); err != nil {
		t.Errorf("hostname is the receiver, userinfo is not a host: %v", err)
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
