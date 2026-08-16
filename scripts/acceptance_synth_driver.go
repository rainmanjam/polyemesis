//go:build ignore

// Driver for scripts/acceptance-synth.sh.
//
// The synth suite runs the pull path against a VIDEO-ONLY file, so the source
// has no audio at all. Everything a destination does with audio -- selecting a
// track, mixing it, encoding it -- then has nothing to work from, and the
// silence tier is what stands in. Without it the destination below has no track
// 0 to select and crash-loops, which is the failure this suite exists to catch.
//
//	(no subcommand)  set up, switch to pull, create the destination
//	tracks           print how many audio tracks were probed
//	stopall          stop every destination so its file finalises
//	slate-escape     try a slate image outside the data directory
//	proclog <name>   print a process's own stderr
//
// The session and the shared subcommands live in pullsynthhelpers.go, named on
// the `go run` line. What is left here is what makes this suite the SYNTH one.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const (
	pass = "SynthAcceptance!9x"

	// Video only: no audio stream at all, which is the whole point.
	pullURL = "file://recordings/videoonly.ts"

	pullModeDone = "PULL_OK"
	destsDone    = "DEST_OK"
)

// One destination, selecting track 0 on a source that HAS no tracks. That is
// the silence tier under test: without it there is nothing to select.
var destFixtures = []destSpec{
	{"synth", "synth.mkv", 0},
}

func main() {
	run("acceptance_synth_driver.go", func(cmd string) bool {
		switch cmd {
		case "slate-escape":
			login()
			slateEscape()
		case "proclog":
			if len(os.Args) < 4 {
				die("usage: proclog <process-name>")
			}
			login()
			procLog(os.Args[3])
		default:
			return false
		}
		return true
	})
}

// procLog prints a process's own stderr, which the server keeps in a ring and
// publishes over the event bus rather than writing to its log file.
//
// Without this a crash-looping process is diagnosed entirely from the outside:
// the server log says it exited and with what status, and the one thing that
// would explain why -- what FFmpeg itself said -- is only visible in a browser.
func procLog(name string) {
	_, out := do(http.MethodGet, "/processes/"+name+"/logs", nil)
	var got struct {
		Lines []struct {
			Text string `json:"text"`
		} `json:"lines"`
	}
	_ = json.Unmarshal(out, &got)
	for _, l := range got.Lines {
		fmt.Println(l.Text)
	}
}

// slateEscape checks the slate image confinement.
//
// The slate path is a file this process opens to paint a holding frame, so an
// unconfined one reads anything the server can. Same reasoning as a pull
// source, and worth an end-to-end check for the same reason: confinement that
// holds in the validator but not in the running server is worth nothing.
func slateEscape() {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	_ = json.Unmarshal(out, &s)
	fo, _ := s["failover"].(map[string]any)
	if fo == nil {
		fo = map[string]any{}
		s["failover"] = fo
	}
	slate, _ := fo["slate"].(map[string]any)
	if slate == nil {
		slate = map[string]any{}
		fo["slate"] = slate
	}
	slate["enabled"] = true
	slate["imagePath"] = "../../../../etc/passwd"

	code, body := do(http.MethodPut, "/settings", s)
	if code == http.StatusOK {
		fmt.Println("SLATE_ACCEPTED")
		return
	}
	fmt.Printf("SLATE_REFUSED %d %s\n", code, strings.TrimSpace(string(body)))
}
