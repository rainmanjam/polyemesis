//go:build ignore

// Driver for scripts/acceptance-audio.sh.
//
// Where acceptance_driver.go proves the ROUTING (each destination gets exactly
// its tracks), this one proves the AUDIO PROCESSING: loudness, delay, ducking,
// audio-only output and stem recording. Everything it creates is measured
// afterwards by the shell script; nothing here asserts, it only stages.
//
// The source is a 3-track stream chosen so every feature can be measured from
// the output alone, without knowing when the capture started:
//
//	video    white 4s / black 4s        IN LOCKSTEP with the mic gate below
//	track 0  300 Hz continuous          the "music" bed, and the duck target
//	track 1  900 Hz continuous          carried plainly, and pulled down hard by
//	                                    the loudness destination alone
//	track 2  2000 Hz 4s on / 4s off     the "mic", and the duck trigger
//
// All three sit at -9 dBFS. High enough that a carried tone clears the
// presence threshold with room to spare, low enough that summing any two of
// them stays under the limiter's ceiling — a limiter engaging on the mic burst
// would pull the music down by itself and would read exactly like a duck.
//
// Two properties are doing all the work here.
//
// The mic's on/off cycle makes ducking measurable: the script finds the windows
// where 2000 Hz is present and compares the 300 Hz energy in those windows
// against the windows where it is absent. Self-locating, so it does not matter
// which second of the stream the destination happened to start on.
//
// The video flashing in lockstep with that same gate makes DELAY measurable
// WITHIN ONE FILE. Both destinations copy the video and only the audio moves,
// so the gap between the video going black and the audio going silent IS the
// A/V offset. Comparing two files' start times would have measured when two
// FFmpeg processes happened to launch, which is not the same question and is
// wrong by more than the delay being tested.
package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var (
	client *http.Client
	base   string
	csrf   string
	// sourceID is the programme this driver created, captured from the create
	// response below. Every destination has to name it: a create that omits
	// sourceId is now refused with 400 source_required, because the server used
	// to fill an omitted source in with the FIRST source and so could attach a
	// destination to a programme nobody chose.
	//
	// Captured rather than hardcoded to 1. Hardcoding would pass today and
	// encodes an autoincrement assumption that stops holding the moment a
	// driver creates a second source or runs against a database that has seen a
	// delete.
	sourceID int64
)

// loudnessTarget is what the loudness destination asks for. -14 LUFS because
// that is the number every operator has actually heard of.
const loudnessTarget = -14.0

// delayMS is the offset the delay destination applies. Large enough to measure
// with confidence against an AAC encoder's own priming, small enough to stay a
// plausible moderation delay.
const delayMS = 400

// negDelayMS is the magnitude of the NEGATIVE delay, i.e. audio pulled ahead of
// picture. Applied as -negDelayMS.
const negDelayMS = 300

// stereo widens a lavfi mono generator to two channels. See the source comment.
const stereo = "aformat=channel_layouts=stereo"

// loudnessGain is how far the loudness destination pulls its track down before
// loudnorm sees it, so the filter has roughly 20 LU to climb. Done here rather
// than by making the SOURCE quiet, because every other check in this suite has
// to be able to hear that same track.
const loudnessGain = 0.1

// waitUp, grabCSRF, call and get live in driverhelpers.go, compiled in by
// naming it on the `go run` line. See that file for why it is not a package.
func main() {
	if len(os.Args) < 3 {
		die("usage: acceptance_audio_driver.go <http-port> <relay-port>")
	}
	port, relay := os.Args[1], os.Args[2]
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	waitUp()
	fmt.Println("first-run setup")
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	// The programme everything below hangs off. A fresh install has none since
	// #387; see acceptance_driver.go's copy of this note for the full reason.
	fmt.Println("creating the first source")
	created := call("POST", "/sources", map[string]any{"name": "Main", "enabled": true})
	sid, ok := created["id"].(float64)
	if !ok || sid == 0 {
		die("created source carried no id: %v", created)
	}
	sourceID = int64(sid)

	// Stems on. This is the one feature here that is a property of the
	// RECORDER rather than of a destination, so it is switched on before the
	// stream starts and read off disk at the end.
	settings := get("/settings")
	rec := settings["recording"].(map[string]any)
	rec["enabled"] = true
	rec["stems"] = true
	rec["stemCodec"] = "flac"
	rec["segmentSeconds"] = 3600
	call("PUT", "/settings", settings)
	fmt.Println("recording with stems enabled (flac)")

	fmt.Println("starting synthetic source (300 Hz music / 900 Hz control / 2000 Hz mic burst)")
	// The shell's lsof is a hint: with no seeded source there may have been
	// no relay socket to find when it looked. ResolveRelayPort asks the
	// server when the hint is empty -- see its comment for the cycle.
	relayPort := resolveRelayPort(relay)
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		// White for the first half of every 8s cycle, black for the second —
		// the same gate the mic runs on, so blackdetect and silencedetect are
		// looking at the same instant in the source.
		"-f", "lavfi", "-i", "color=c=black:s=640x360:r=30,"+
			"drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable='lt(mod(t\\,8)\\,4)'",
		// STEREO, not mono. lavfi's sine is one channel, and a mono ingest
		// track compiles to a different (correct) pan — c1 fed from c0 — which
		// would make the golden strings in the compatibility check disagree
		// with every unit test in the repo for a reason that has nothing to do
		// with this workstream.
		//
		// The music bed: the duck target, and loud enough that a duck on it is
		// unmistakable.
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000,volume=-9dB,"+stereo,
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000,volume=-9dB,"+stereo,
		// The mic: 2000 Hz gated 4s on, 4s off, by multiplying the tone with a
		// square wave derived from the timestamp. lt(mod(t,8),4) is 1 for the
		// first half of every 8-second cycle.
		"-f", "lavfi", "-i", "aevalsrc=sin(2*PI*2000*t)*lt(mod(t\\,8)\\,4):s=48000,volume=-9dB,"+stereo,
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "2000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", "90",
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()

	fmt.Println("waiting for the engine to probe the track layout")
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		s := get("/source")
		tracks, _ := s["tracks"].([]any)
		if s["probed"] == true && len(tracks) == 3 {
			fmt.Printf("probed: %d audio tracks\n", len(tracks))
			goto probed
		}
	}
	die("engine never probed 3 audio tracks")

probed:
	// Roles, so the stems are named by what the tracks ARE and the exclusion
	// test below has something to exclude. This is the per-ingest annotations
	// endpoint, not a per-destination setting.
	call("PUT", "/source/annotations", map[string]any{
		"annotations": []map[string]any{
			{"track": 0, "role": "music", "label": "Bed"},
			{"track": 1, "role": "commentary", "label": "Control"},
			{"track": 2, "role": "mic", "label": "Mic"},
		},
	})
	fmt.Println("track roles recorded (music / commentary / mic)")

	fmt.Println("creating destinations")

	// 1. LOUDNESS. One track, pulled well below the target, with loudnorm armed
	// by an explicit target it now has to climb to.
	p := profile([]int{1}, loudnessGain)
	p["normalize"] = "loudnorm"
	p["loudness"] = map[string]any{"targetLufs": loudnessTarget, "truePeakDb": -1.0}
	call("POST", "/destinations", dest("Loudness", "file", "loudness.mkv", p))

	// 2 and 3. DELAY, measured as the difference between two otherwise
	// identical destinations. Comparing two outputs of the same run removes
	// the encoder's own priming delay from the answer, which is the whole
	// reason there is a reference at all.
	//
	// The mic track is the one carried: its 4s-on/4s-off edge is the transient
	// the script locates.
	call("POST", "/destinations", dest("Delay reference", "file", "delay-ref.mkv",
		profile([]int{2}, 1.0)))
	pd := profile([]int{2}, 1.0)
	pd["delayMs"] = delayMS
	call("POST", "/destinations", dest("Delayed", "file", "delayed.mkv", pd))

	// The other direction, which is a different mechanism entirely. No audio
	// filter can pull sound AHEAD of picture, so a negative delay compiles to
	// nothing in the graph and holds the VIDEO back instead, on the
	// destination's command line. It leaves no trace in the filter string,
	// which is exactly why it needs measuring rather than reading.
	pn := profile([]int{2}, 1.0)
	pn["delayMs"] = -negDelayMS
	call("POST", "/destinations", dest("Pulled ahead", "file", "neg-delay.mkv", pn))

	// 4 and 5. DUCKING, against a control carrying the identical mix with no
	// duck. The control is what makes the result mean something: anything that
	// pulls the music down when the mic arrives — a limiter, amix's own
	// normalisation, an encoder — would show up in BOTH files, so only the
	// difference between them can be attributed to the duck.
	call("POST", "/destinations", dest("Duck control", "file", "duck-control.mkv",
		profile([]int{0, 2}, 1.0)))
	pk := profile([]int{0, 2}, 1.0)
	pk["ducking"] = map[string]any{
		"trigger": []int{2}, "target": []int{0},
		"thresholdDb": -30.0, "ratio": 20.0, "attackMs": 20.0, "releaseMs": 250.0,
	}
	call("POST", "/destinations", dest("Ducked", "file", "ducked.mkv", pk))

	// 6. AUDIO-ONLY. Same mix as the routing suite's file destination, with the
	// video half deleted.
	call("POST", "/destinations", dest("Audio only", "audio", "audio-only.m4a",
		profile([]int{0, 1}, 1.0)))

	// 7. ROLE EXCLUSION. The DMCA switch: carry everything, then drop whatever
	// is annotated music. Track 0 is the music, so 300 Hz must be absent and
	// 900 Hz must survive.
	pe := profile([]int{0, 1}, 1.0)
	pe["excludeRoles"] = []string{"music"}
	call("POST", "/destinations", dest("No music", "file", "no-music.mkv", pe))

	// 8. COPY, WITH A ROLE EXCLUSION ON TOP. #144. The two claims that matter
	// are that the tracks arrive SEPARATELY -- not summed into one stereo pair,
	// which is what every other destination here does -- and that the DMCA
	// switch still works when nothing is being mixed. So tracks 0, 1 and 2 are
	// all selected and "music" is excluded: two tracks must come out, carrying
	// 900 Hz and 2000 Hz, and the 300 Hz music bed must be nowhere in the file.
	//
	// Normalization is set to off explicitly. "auto" would also be accepted --
	// it means "no opinion" and resolves to nothing without a sum to protect --
	// but a driver that relied on that would stop testing the day the default
	// changed.
	pc := profile([]int{0, 1, 2}, 1.0)
	pc["normalize"] = "off"
	pc["excludeRoles"] = []string{"music"}
	copyDest := dest("Copied", "file", "copied.mkv", pc)
	copyDest["audio"] = map[string]any{"copy": true}
	call("POST", "/destinations", copyDest)

	fmt.Println("waiting for every destination to run")
	want := 9
	deadline = time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		st := get("/status")
		running := 0
		for _, d := range st["destinations"].([]any) {
			dm := d.(map[string]any)
			if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
				running++
			}
		}
		if running == want {
			for _, d := range st["destinations"].([]any) {
				dm := d.(map[string]any)
				fmt.Printf("  %-18s %s\n", dm["name"], dm["summary"])
			}
			goto running
		}
	}
	die("destinations never all reached running")

running:
	// Long enough to contain at least two full mic cycles, so the ducking
	// measurement has several on-windows and several off-windows to average
	// over rather than one of each.
	fmt.Println("streaming for 26s")
	time.Sleep(26 * time.Second)

	fmt.Println("stopping destinations cleanly")
	for i := 1; i <= want; i++ {
		call("POST", "/destinations/"+strconv.Itoa(i)+"/stop", nil)
	}
	// Recording off before the source dies, so the recorder and its stems are
	// closed by a clean shutdown rather than by a vanishing input.
	settings = get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)
	time.Sleep(5 * time.Second)
	fmt.Println("driver done")
}

// profile builds a simple-mode profile carrying the named tracks at gain.
func profile(tracks []int, gain float64) map[string]any {
	on := map[int]bool{}
	for _, t := range tracks {
		on[t] = true
	}
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		g := gain
		if !on[i] {
			g = 1.0
		}
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": g})
	}
	return map[string]any{
		"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
	}
}

func dest(name, kind, url string, prof map[string]any) map[string]any {
	return map[string]any{
		"name": name, "kind": kind, "platform": "custom", "url": url,
		"enabled": true, "audioBitrate": 160, "profile": prof,
		"sourceId": sourceID,
	}
}

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	os.Exit(1)
}

// resolveRelayPort: the shell's lsof is a hint, not a precondition. Without a
// seeded source no relay socket exists until this driver creates one, so an
// empty value means "ask the server", not "fail". The full account of the cycle
// is in driverlib.ResolveRelayPort; this file cannot import it, because `go run`
// resolves module imports against the cwd and these suites run from /tmp.
func resolveRelayPort(fromShell string) int {
	if p, err := strconv.Atoi(strings.TrimSpace(fromShell)); err == nil && p > 0 {
		return p
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		relay, _ := get("/stats")["relay"].(map[string]any)
		if pf, ok := relay["port"].(float64); ok && pf > 0 {
			return int(pf)
		}
	}
	die("no relay port after 30s; the source was probably never created")
	return 0
}
