//go:build ignore

// Driver for scripts/acceptance-audio-drift.sh.
//
// The suite measures one thing: what a destination that is NOT restarted at a
// format-changing failover sends after it. The selector keeps such a
// destination on the same hub, the backup's audio arrives at another sample
// rate and channel count, and FFmpeg rebuilds the destination's filter graph
// from its argv in the middle of the run.
//
// The HTTP session, login, setup and the settings read-modify-write live in
// scripts/internal/driverlib. What stays here is what this suite alone does:
// a failover with a BACKUP ingest (not a slate), two RTMP destinations, a live
// gain change on one of them, and the restart counts the shell decides on.
//
//	setup <backup|slate>  set up, enable failover onto a backup ingest or a slate
//	publishkey            print the source's publish key (the backup's is <key>.backup)
//	adddest <name> <url> [tracks]
//	                      add one RTMP destination; tracks is how many of the
//	                      ingest's tracks it mixes, from track 0 (default 1)
//	gain <name> <gain>    change track 0's gain live on one destination
//	pin <kind>            put a source on air by hand (primary|backup|slate|auto)
//	status                print "<active> <switches>"
//	stopall               stop every destination, so each flushes its last audio
//	restarts <name>       print one destination's restart count, or -1
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

const (
	user = "admin"
	pass = "AudioDriftAcceptance!9x"
	// ingestPort is the RTMP listener INSIDE the container; the shell maps it.
	ingestPort = 1935
)

func main() {
	if len(os.Args) < 3 {
		driverlib.Die("usage: acceptance_audio_drift_driver.go <base-url> <subcommand>")
	}
	driverlib.Init(os.Args[1])
	driverlib.WaitUp()

	switch cmd := os.Args[2]; cmd {
	case "setup":
		need(4, "setup <backup|slate>")
		driverlib.Setup(user, pass)
		driverlib.EnsureSource("Main")
		enableFailover(os.Args[3] == "slate")
	case "pin":
		need(4, "pin <primary|backup|slate|auto>")
		driverlib.Login(user, pass)
		code, out := driverlib.Do(http.MethodPost, "/failover/source", map[string]any{"source": os.Args[3]})
		if code != http.StatusOK {
			driverlib.Die(fmt.Sprintf("pin %s failed: %d %s", os.Args[3], code, out))
		}
		fmt.Println("PIN_OK")
	case "publishkey":
		driverlib.Login(user, pass)
		driverlib.PublishKey()
	case "adddest":
		need(5, "adddest <name> <rtmp-url> [tracks]")
		driverlib.Login(user, pass)
		tracks := 1
		if len(os.Args) > 5 {
			n, err := strconv.Atoi(os.Args[5])
			if err != nil || n < 1 || n > driverlib.ProfileTracks {
				driverlib.Die("tracks must be 1.." + strconv.Itoa(driverlib.ProfileTracks) + ", not " + os.Args[5])
			}
			tracks = n
		}
		addDest(os.Args[3], os.Args[4], tracks)
	case "gain":
		need(5, "gain <name> <gain>")
		driverlib.Login(user, pass)
		setGain(os.Args[3], os.Args[4])
	case "stopall":
		driverlib.Login(user, pass)
		driverlib.StopAll()
	case "status":
		driverlib.Login(user, pass)
		st := readStatus()
		if st.Failover == nil {
			fmt.Println("none -1")
			return
		}
		fmt.Printf("%s %d\n", st.Failover.Active, st.Failover.Switches)
	case "restarts":
		need(4, "restarts <name>")
		driverlib.Login(user, pass)
		for _, d := range readStatus().Destinations {
			if d.Name == os.Args[3] && d.Process != nil {
				fmt.Println(d.Process.Restarts)
				return
			}
		}
		fmt.Println(-1)
	default:
		driverlib.Die("unknown subcommand " + cmd)
	}
}

func need(n int, usage string) {
	if len(os.Args) < n {
		driverlib.Die("usage: " + usage)
	}
}

// enableFailover turns the selector on with EITHER a backup ingest or a slate.
//
// Both change the decoded format in this suite. A backup encoder is a
// different machine with its own settings, which is where a 44.1 kHz mono feed
// behind a 48 kHz stereo primary comes from. The slate is always synthesised at
// 48 kHz stereo, so it changes the format under any primary that is not, and
// the slate run publishes a 44.1 kHz mono primary for that reason.
//
// Manual return, so the run decides when the primary comes back (pin primary).
func enableFailover(slate bool) {
	s := driverlib.LoadSettings()
	driverlib.UseRTMPIngest(s, ingestPort)
	s["failover"] = map[string]any{
		"enabled":             true,
		"graceSeconds":        2,
		"return":              "manual",
		"returnStableSeconds": 3,
		"backup": map[string]any{
			"enabled": !slate,
			"mode":    "rtmp",
			"rtmp":    map[string]any{"app": "live", "streamKey": ""},
		},
		"slate": map[string]any{"enabled": slate, "color": "blue", "videoKbps": 800},
	}
	driverlib.SaveSettings(s, "enable failover")
	fmt.Println("FAILOVER_OK")
}

// addDest creates one RTMP destination mixing tracks 0..tracks-1.
//
// More than one is the MULTI-track graph, whose every leg ends in routing's
// trackAlign (first_pts=0) -- the shape a format-changing switch used to put
// the stream's runtime in front of, because a rebuilt leg pads from zero.
//
// Normalisation off, so nothing between the source and the sink smooths the
// level difference the shell uses to tell the primary's tone from the
// backup's. The URL names the sink's path; the sink accepts any key.
func addDest(name, url string, tracks int) {
	on := make([]int, tracks)
	for i := range on {
		on[i] = i
	}
	driverlib.CreateDest(name, map[string]any{
		"name": name, "kind": "rtmp", "platform": "custom",
		"url": url, "streamKey": name,
		"enabled": true, "audioBitrate": 128,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(on...), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	})
}

// setGain sends a live level change, which the engine applies over the
// destination's command channel without restarting it. It leaves that
// destination's argv carrying the OLD level, so the engine restarts it at the
// next feed change (respawnStaleLevels) -- the destination the suite uses as
// the restarted control.
func setGain(name, gain string) {
	g, err := strconv.ParseFloat(gain, 64)
	if err != nil {
		driverlib.Die("gain is not a number: " + gain)
	}
	id := destID(name)
	code, out := driverlib.Do(http.MethodPost,
		fmt.Sprintf("/destinations/%d/tracks/0/gain", id), map[string]any{"gain": g})
	if code != http.StatusOK {
		driverlib.Die(fmt.Sprintf("gain %s failed: %d %s", name, code, out))
	}
	fmt.Println("GAIN_OK")
}

func destID(name string) int64 {
	_, out := driverlib.Do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		driverlib.Die("destinations unreadable: " + err.Error())
	}
	for _, r := range rows {
		if r.Destination.Name == name {
			return r.Destination.ID
		}
	}
	driverlib.Die("no destination named " + name)
	return 0
}

type statusDoc struct {
	Failover *struct {
		Active   string `json:"active"`
		Switches int    `json:"switches"`
	} `json:"failover"`
	Destinations []struct {
		Name    string `json:"name"`
		Process *struct {
			Restarts int `json:"restarts"`
		} `json:"process"`
	} `json:"destinations"`
}

func readStatus() statusDoc {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
	return st
}
