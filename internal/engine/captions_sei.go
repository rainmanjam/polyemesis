package engine

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// SEI CAPTIONS IN A DESTINATION'S PATH (#8).
//
// A destination that asks for SEI captions reads through a captions.SEIStage
// that sits between the stream it would otherwise read and its FFmpeg:
//
//	upstream hub (or the delay line's private hub, for a delayed destination)
//	    -> UDP socket -> SEIStage.Pump -> capHub -> the destination's FFmpeg (-c copy)
//
// The stage is one injection point, after the broadcast delay, so a delayed
// destination's captions are placed on the delayed pictures. Video is copied:
// the stage rewrites only the H.264 elementary stream, adding one caption SEI per
// picture, and FFmpeg still runs with -c copy.
//
// The stage is decided from the SAVED row, not from whether the captioner is
// running. With no cue queued it carries null pairs, so a health-guard degrade
// of the captioner never touches the destination. An operator save of the caption
// mode changes the destination's spec, which restarts it: the same precedent as
// a delay change.

// destCaption is one destination's running SEI stage and the plumbing around it.
type destCaption struct {
	id    int64
	stage *captions.SEIStage
	// conn is the stage's input socket, the port the upstream hub sends to.
	conn net.PacketConn
	port int
	// hub is the stage's output hub. The destination's FFmpeg and its backup
	// subscribe to it.
	hub *relay.Hub
	// upstream is the hub the stage reads, under inSub.
	upstream *relay.Hub
	inSub    string
	cancel   context.CancelFunc
	done     chan struct{}
}

// wantsSEIStage reports whether a saved destination runs through an SEI stage.
// It uses the same gate as the captioner's intent (captions.Allowed), so a
// destination is never staged for a platform the captioner would not caption.
func wantsSEIStage(row *db.Destination, facebookOpen bool) bool {
	if row == nil {
		return false
	}
	mode := captions.Mode(row.CaptionMode)
	if mode != captions.ModeSEI && mode != captions.ModeFacebookSEI {
		return false
	}
	return captions.Allowed(string(row.Platform), mode, facebookOpen)
}

// startCaptionStage builds and starts a destination's SEI stage, reading the
// upstream hub it is given. It returns nil, nil when the row does not want one.
// On any error everything it took is released, so the caller only has to unwind
// what it itself did.
func (e *Engine) startCaptionStage(row *db.Destination, upstream *relay.Hub) (*destCaption, error) {
	if !wantsSEIStage(row, e.facebookCaptions.Load()) {
		return nil, nil
	}
	outHub, err := relay.New(e.log, 0)
	if err != nil {
		return nil, fmt.Errorf("captions: %w", err)
	}
	// BOUND HERE, ON :0, AND HELD -- not taken from e.alloc and bound after.
	// The allocator answers "free when I probed it" by binding the port and
	// closing it, and between that probe and a bind here the port belongs to
	// nobody; another process on the same range can take it, and the stage
	// fails with "address already in use". The allocator is for ports a child
	// must be told before it binds; this socket is in-process. See buildDelay.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		_ = outHub.Close()
		return nil, fmt.Errorf("captions: %w", err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	capt := &destCaption{
		id:       row.ID,
		stage:    captions.NewSEIStage(),
		conn:     conn,
		port:     port,
		hub:      outHub,
		upstream: upstream,
		inSub:    destSubName(row.ID, "caption"),
		done:     make(chan struct{}),
	}
	if _, err := upstream.Subscribe(capt.inSub, port); err != nil {
		_ = conn.Close()
		_ = outHub.Close()
		return nil, err
	}
	name := row.Name
	capt.stage.OnUnsupported(func() {
		e.log.Warn("captions: this source's video is HEVC, and SEI captions are carried only on H.264, so this "+
			"destination's stream goes out without captions; send H.264 or choose another caption mode",
			"destination", name)
	})
	ctx, cancel := context.WithCancel(context.Background())
	capt.cancel = cancel
	go func() {
		defer close(capt.done)
		// Pump's error is the socket closing on teardown; it is not a fault to
		// report, so it is dropped here on purpose.
		_ = capt.stage.Pump(ctx, conn, outHub.Deliver)
	}()
	e.seiStages.Store(row.ID, capt.stage)
	return capt, nil
}

// closeCaptionStage stops a stage and releases what startCaptionStage took. It
// is safe on nil and on a stage already closed. The order matters: the pump is
// stopped and waited for first, so its final flush reaches the output hub before
// that hub closes.
func (e *Engine) closeCaptionStage(capt *destCaption) {
	if capt == nil {
		return
	}
	if capt.cancel != nil {
		capt.cancel()
	}
	if capt.conn != nil {
		_ = capt.conn.Close()
	}
	<-capt.done
	capt.upstream.Unsubscribe(capt.inSub)
	_ = capt.hub.Close()
	// Only this stage's own registration is removed. A restart may have
	// registered a newer stage under the same id, which must be left alone.
	e.seiStages.CompareAndDelete(capt.id, capt.stage)
}

// captionSink is the production CueSink. The CueRouter calls Deliver from its
// timers, so it only looks the stage up and queues the text. It takes no engine
// lock, which keeps it clear of the lock order the reconcile path holds.
type captionSink struct{ e *Engine }

// Deliver queues a caption on the destination's POST queue or SEI stage. A
// destination with neither (none registered yet) drops the caption.
func (s captionSink) Deliver(id int64, c transcribe.LiveCaption) {
	// A destination posting to YouTube: queue the cue, stamped with the moment
	// its words reached YouTube's ingest. Never blocks.
	if q, ok := s.e.postQueues.Load(id); ok {
		q.(*captions.PostQueue).Enqueue(postStamp(c), c.Text)
		return
	}
	v, ok := s.e.seiStages.Load(id)
	if !ok {
		return
	}
	dur := time.Duration(c.EndMS-c.StartMS) * time.Millisecond
	if dur <= 0 {
		dur = 4 * time.Second
	}
	v.(*captions.SEIStage).Cue(c.Text, dur)
}

// postStamp is the timestamp a cue posted to YouTube carries: the wall-clock
// moment its words were spoken (LiveCaption.SpokenAt).
//
// YouTube lines a posted caption up by its timestamp against the media it
// ingested, and holds both for the broadcast's own 30 s or 60 s delay. This
// destination's output is not delayed by polyemesis (db.OutputDelaySeconds), so
// the media carrying the words reached YouTube when they were spoken, give or
// take the encoder. The cue is released at once, so time.Now() would stamp it
// the recognition lag plus the cue's own length late.
//
// A cue with no production time (a replay, a test) has no SpokenAt, and stamping
// year one would put it outside YouTube's window, so it is stamped now.
//
// PROVISIONAL (docs-youtube C8): that YouTube reads the timestamp as the wall
// clock of the ingested media is the third-party description, not Google's. A
// live check on a test channel confirms it.
func postStamp(c transcribe.LiveCaption) time.Time {
	if c.At.IsZero() {
		return time.Now()
	}
	return c.SpokenAt()
}
