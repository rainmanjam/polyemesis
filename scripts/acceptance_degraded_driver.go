//go:build ignore

// Driver for scripts/acceptance-degraded.sh: degraded-link failover (#6)
// against a real SRT encoder whose uplink is throttled and made lossy with tc.
//
// The unit and sweep tests drive the monitor with byte counts a test wrote.
// This drives it with what a congested cellular uplink actually does to an SRT
// session -- packets queued, dropped, retransmitted -- and reads the verdict
// back through the same /status a dashboard reads.
//
//	(no subcommand)  set up, enable failover with a slate and degraded thresholds
//	token            print the programme's publish token (the SRT streamid)
//	status           print "<active> <primaryLive> <switches>|<degraded>|<reason>"
package main

import (
	"fmt"
	"os"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

const (
	user = "admin"
	pass = "DegradedAcceptance!7q"
	// The thresholds the suite throttles across. 1000 kbps sits well under the
	// 2500 kbps the encoder sends and well over the 400 kbit tc leaves it.
	minKbps        = 1000
	maxLossPercent = 8
	sustainSeconds = 3
	returnStable   = 3
)

func main() {
	if len(os.Args) < 2 {
		driverlib.Die("usage: acceptance_degraded_driver.go <base-url> [subcommand]")
	}
	driverlib.Init(os.Args[1])
	driverlib.WaitUp()
	cmd := ""
	if len(os.Args) > 2 {
		cmd = os.Args[2]
	}
	switch cmd {
	case "":
		driverlib.Setup(user, pass)
		driverlib.EnsureSource("Main")
		enable()
	case "token":
		driverlib.Login(user, pass)
		driverlib.PublishKey()
	case "status":
		driverlib.Login(user, pass)
		status()
	default:
		driverlib.Die("unknown subcommand " + cmd)
	}
}

func enable() {
	s := driverlib.LoadSettings()
	s["failover"] = map[string]any{
		"enabled":             true,
		"graceSeconds":        3,
		"return":              "auto",
		"returnStableSeconds": returnStable,
		"backup":              map[string]any{"enabled": false},
		"slate":               map[string]any{"enabled": true, "color": "blue", "videoKbps": 800},
		"degraded": map[string]any{
			"enabled":        true,
			"minBitrateKbps": minKbps,
			"maxRttMs":       0,
			"maxLossPercent": maxLossPercent,
			"sustainSeconds": sustainSeconds,
		},
	}
	driverlib.SaveSettings(s, "enable degraded-link failover")
	fmt.Println("DEGRADED_OK")
}

type statusDoc struct {
	Failover *struct {
		Active      string `json:"active"`
		Reason      string `json:"reason"`
		Switches    int    `json:"switches"`
		PrimaryLive bool   `json:"primaryLive"`
		Degraded    string `json:"degraded"`
	} `json:"failover"`
}

func status() {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
	if st.Failover == nil {
		fmt.Println("none false -1||")
		return
	}
	f := st.Failover
	fmt.Printf("%s %t %d|%s|%s\n", f.Active, f.PrimaryLive, f.Switches, f.Degraded, f.Reason)
}
