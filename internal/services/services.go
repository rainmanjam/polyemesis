// Package services is the registry of streaming platforms polyemesis knows how
// to publish to: their ingest servers, the encoder ceilings they enforce, and
// the codecs they accept.
//
// WHY THIS EXISTS. An operator used to type an ingest URL by hand, and a
// mistyped one does not fail loudly -- it connects, or half-connects, and the
// platform drops it. That cost a whole debugging session against Kick, whose
// dashboard shows
//
//	rtmps://fa723fc1b171.global-contribute.live-video.net/
//
// with no application path. Pasted verbatim it composes rtmps://<host>/<key>,
// which makes the stream key the RTMP *app name* and leaves no stream name at
// all. Amazon IVS, which Kick runs on, refuses that: the destination reports
// "reconnecting" forever, produces zero output, and nothing anywhere says why.
//
// OBS ships the same knowledge as data rather than as advice, and this file is
// seeded from it -- see services.json for provenance. The number that settles
// the design: of the 540 ingest URLs OBS lists, 529 carry an application path.
// A publish URL without one is the exception, not a style choice, which is what
// makes AnalyseURL's warning worth showing.
package services

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

//go:embed services.json
var registryJSON []byte

// Server is one ingest endpoint. Name is what an operator picks from; URL is
// what gets published to, and always carries an application path.
type Server struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Recommended is the platform's own encoder ceiling. A zero field means the
// platform publishes no figure, NOT that the limit is zero -- every consumer
// has to check before comparing, so the guards below all do.
type Recommended struct {
	KeyintSeconds int    `json:"keyintSeconds,omitempty"`
	MaxVideoKbps  int    `json:"maxVideoKbps,omitempty"`
	MaxAudioKbps  int    `json:"maxAudioKbps,omitempty"`
	MaxFps        int    `json:"maxFps,omitempty"`
	X264Opts      string `json:"x264opts,omitempty"`
}

// Service is one platform. ID matches db.Platform's string values so a
// destination's platform column selects a registry entry directly; the two are
// kept in step by TestEveryPlatformHasARegistryEntry over in internal/db.
type Service struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// PerChannelIngest platforms issue a different ingest host per channel,
	// so Servers is empty and the operator must supply the URL. Kick is the
	// only one today, and it is also the only one this registry cannot
	// prevent a typo in -- hence Note.
	PerChannelIngest bool        `json:"perChannelIngest,omitempty"`
	Servers          []Server    `json:"servers,omitempty"`
	Recommended      Recommended `json:"recommended"`
	VideoCodecs      []string    `json:"videoCodecs,omitempty"`
	AudioCodecs      []string    `json:"audioCodecs,omitempty"`
	StreamKeyLink    string      `json:"streamKeyLink,omitempty"`
	Note             string      `json:"note,omitempty"`
}

type registry struct {
	Provenance string    `json:"provenance"`
	Services   []Service `json:"services"`
}

var (
	loadOnce sync.Once
	loaded   registry
	loadErr  error
)

func load() (registry, error) {
	loadOnce.Do(func() {
		if err := json.Unmarshal(registryJSON, &loaded); err != nil {
			loadErr = fmt.Errorf("services registry is malformed: %w", err)
		}
	})
	return loaded, loadErr
}

// All returns every service, ordered by name so the UI does not have to sort.
func All() []Service {
	r, err := load()
	if err != nil {
		return nil
	}
	out := make([]Service, len(r.Services))
	copy(out, r.Services)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Provenance is where this data came from, carried so the API can say so
// rather than presenting a copied table as if we had measured it ourselves.
func Provenance() string {
	r, _ := load()
	return r.Provenance
}

// Lookup finds a service by ID. The bool is false for "custom" and for any
// platform with no registry entry, which callers must treat as "no opinion"
// rather than as an error -- a custom destination is a legitimate thing.
func Lookup(id string) (Service, bool) {
	r, err := load()
	if err != nil {
		return Service{}, false
	}
	for _, s := range r.Services {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// URLProblem is an advisory finding about a publish URL: something that will
// probably not work, stated as a warning rather than an error because the
// registry cannot prove it. 11 of OBS's 540 ingest URLs really do have no
// application path, so refusing outright would break a real, if rare, setup.
type URLProblem struct {
	// Field names the input to blame, so a UI can highlight it.
	Field string
	// Detail is written for the operator, not for a log.
	Detail string
	// Fix is the corrected URL when one can be derived, else empty. Offered
	// rather than applied: silently rewriting what somebody typed is how you
	// get a bug report that says "it changed my URL".
	Fix string
}

// AnalyseURL reports advisory problems with an RTMP publish URL. It never
// rejects: Destination.Validate owns refusal, and refusal has to stay narrow.
//
// Scheme errors are deliberately NOT reported here -- Validate already fails
// those, and a second voice saying the same thing in different words is how
// two guards drift apart.
func AnalyseURL(raw string) []URLProblem {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil
	}
	if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
		return nil
	}

	var probs []URLProblem
	if strings.Trim(u.Path, "/") == "" {
		fixed := *u
		fixed.Path = "/app"
		probs = append(probs, URLProblem{
			Field: "url",
			Detail: "this URL has no application path. Almost every RTMP service " +
				"needs one (Twitch uses /app, YouTube /live2, Facebook /rtmp). " +
				"Without it the stream key becomes the application name and the " +
				"far end will refuse the publish, usually by silently dropping " +
				"the connection.",
			Fix: fixed.String(),
		})
	}
	// A key already in the path is the other half of the same mistake: the
	// operator pasted "server + key" into the box meant for the server, and
	// the key gets appended a second time. Cheap to spot, because a real
	// application path is a short word and a stream key is not.
	if seg := strings.Trim(u.Path, "/"); len(seg) > 40 && !strings.Contains(seg, "/") {
		probs = append(probs, URLProblem{
			Field: "url",
			Detail: "the path segment in this URL is long enough to be a stream " +
				"key. The server URL and the stream key are separate fields " +
				"here; if the key is in both, it will be sent twice.",
		})
	}
	return probs
}

// CheckEncoder reports where a destination's settings exceed what the platform
// accepts. Zero ceilings mean "no published figure" and are skipped, so a
// service we know little about produces no noise.
func CheckEncoder(svc Service, audioKbps, videoKbps, fps int) []URLProblem {
	var probs []URLProblem
	add := func(field, f string, a ...any) {
		probs = append(probs, URLProblem{Field: field, Detail: fmt.Sprintf(f, a...)})
	}
	if m := svc.Recommended.MaxAudioKbps; m > 0 && audioKbps > m {
		add("audioBitrate", "%s accepts at most %d kbps of audio; this destination sends %d. "+
			"The platform will re-encode or reject it.", svc.Name, m, audioKbps)
	}
	if m := svc.Recommended.MaxVideoKbps; m > 0 && videoKbps > m {
		add("videoBitrate", "%s accepts at most %d kbps of video; this destination sends %d.",
			svc.Name, m, videoKbps)
	}
	if m := svc.Recommended.MaxFps; m > 0 && fps > m {
		add("fps", "%s accepts at most %d fps; this destination sends %d.", svc.Name, m, fps)
	}
	return probs
}
