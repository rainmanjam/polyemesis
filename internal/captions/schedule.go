package captions

import (
	"sort"
	"strings"
	"time"
)

// ticksPerSecond is the MPEG-TS presentation clock, 90 kHz.
const ticksPerSecond = 90000

// Cue is one caption on the stream's presentation timeline. Times are 90 kHz
// ticks, the same clock the picture timestamps use, so a cue lands on the
// picture whose timestamp it matches and nowhere else.
type Cue struct {
	StartPTS int64
	EndPTS   int64
	Text     string
}

// event is a run of pairs due at a timestamp.
type event struct {
	at    int64
	pairs []Pair
}

// Schedule turns caption cues into one CEA-608 byte pair per picture.
//
// The 608 channel carries about one pair per field, so a pop-on script of forty
// or fifty pairs takes a couple of seconds to go out. The schedule is therefore
// a queue: cues add their script at their start time and their erase at their
// end time, and each picture takes the next pair off the front. Pairs keep
// their order, so a caption's erase never overtakes its own text.
//
// A cue registered after its start time is not dropped. It goes out at the next
// picture, late, which is the honest outcome when captioning lags the picture.
type Schedule struct {
	resendEvery int64
	events      []event
	queue       []Pair
}

// NewSchedule returns an empty schedule. A positive resendEvery re-sends each
// pop-on script at that interval while its cue is on screen, so a viewer who
// joins an HLS stream mid-caption sees it within one interval. Zero disables
// the resend.
func NewSchedule(resendEvery time.Duration) *Schedule {
	return &Schedule{resendEvery: int64(resendEvery.Seconds() * ticksPerSecond)}
}

// Add registers a cue. Blank text schedules nothing, so an empty line never
// clears the screen on every resend interval.
func (s *Schedule) Add(c Cue) {
	if strings.TrimSpace(c.Text) == "" {
		return
	}
	s.insert(event{at: c.StartPTS, pairs: PopOnScript(c.Text)})
	if s.resendEvery > 0 {
		for at := c.StartPTS + s.resendEvery; at < c.EndPTS; at += s.resendEvery {
			s.insert(event{at: at, pairs: PopOnScript(c.Text)})
		}
	}
	s.insert(event{at: c.EndPTS, pairs: ClearScript()})
}

// insert keeps events ordered by due time. Equal times keep arrival order, so an
// erase registered before a script at the same instant still runs first.
func (s *Schedule) insert(e event) {
	i := sort.Search(len(s.events), func(i int) bool { return s.events[i].at > e.at })
	s.events = append(s.events, event{})
	copy(s.events[i+1:], s.events[i:])
	s.events[i] = e
}

// NextPair returns the pair for the picture with presentation time pts. Call it
// once per picture, in the order pictures are injected. Every event due at or
// before pts moves into the queue first, then the front pair goes out, and the
// null pair is sent when the queue is empty.
func (s *Schedule) NextPair(pts int64) Pair {
	n := 0
	for n < len(s.events) && s.events[n].at <= pts {
		s.queue = append(s.queue, s.events[n].pairs...)
		n++
	}
	s.events = s.events[n:]
	if len(s.queue) == 0 {
		return nullPair
	}
	p := s.queue[0]
	s.queue = s.queue[1:]
	return p
}
