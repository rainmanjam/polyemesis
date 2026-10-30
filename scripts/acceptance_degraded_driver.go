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
//	srtla <port>     set the SRTLA receiver's port (0 turns it off)
//	bonded           print "<publishing> <bonded-links>" for the programme
package main

import (
	"fmt"
	"os"
	"strconv"

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
	case "srtla":
		if len(os.Args) < 4 {
			driverlib.Die("usage: srtla <port>")
		}
		driverlib.Login(user, pass)
		setSRTLAPort(os.Args[3])
	case "bonded":
		driverlib.Login(user, pass)
		bonded()
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
	}
	// The thresholds are the PROGRAMME's, on its ingest; the settings save
	// writes the ingest through to the default programme the suite created.
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		driverlib.Die("settings carry no ingest block")
	}
	ing["degraded"] = map[string]any{
		"enabled":        true,
		"minBitrateKbps": minKbps,
		"maxRttMs":       0,
		"maxLossPercent": maxLossPercent,
		"sustainSeconds": sustainSeconds,
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

func setSRTLAPort(arg string) {
	port, err := strconv.Atoi(arg)
	if err != nil {
		driverlib.Die("srtla port: " + err.Error())
	}
	s := driverlib.LoadSettings()
	l, _ := s["listeners"].(map[string]any)
	if l == nil {
		driverlib.Die("settings carry no listeners block")
	}
	l["srtlaPort"] = port
	driverlib.SaveSettings(s, "set the SRTLA port")
	fmt.Println("SRTLA_OK")
}

// bonded reads the programme's card the way the Sources page does: whether an
// encoder is publishing, and how many links its SRTLA group has.
func bonded() {
	var rows []struct {
		Publishing bool `json:"publishing"`
		Bonded     *struct {
			Links []struct {
				Peer    string `json:"peer"`
				Packets uint64 `json:"packets"`
			} `json:"links"`
		} `json:"bonded"`
	}
	driverlib.GetJSON("/sources", "sources", &rows)
	if len(rows) == 0 {
		driverlib.Die("no programme")
	}
	n := 0
	if b := rows[0].Bonded; b != nil {
		for _, l := range b.Links {
			if l.Packets > 0 {
				n++
			}
		}
	}
	fmt.Printf("%t %d\n", rows[0].Publishing, n)
}
