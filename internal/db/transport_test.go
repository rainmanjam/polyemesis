package db

import (
	"strings"
	"testing"
)

func transportDest() *Destination {
	d := validDest()
	d.Transport = DestTransport{
		NoDurationFilesize: true,
		MuxQueuePackets:    2048,
		MuxQueueBytes:      8 << 20,
		RWTimeoutSeconds:   10,
	}
	return d
}

func TestAValidTransportBlockIsAccepted(t *testing.T) {
	// The positive case. Without it every refusal below would be satisfied by
	// a validator that refused everything.
	if err := transportDest().Validate(); err != nil {
		t.Fatalf("a well-formed transport block was refused: %v", err)
	}
	// And the zero value must stay valid, or every pre-existing destination
	// becomes unsavable.
	d := validDest()
	if err := d.Validate(); err != nil {
		t.Fatalf("a destination with no transport tuning was refused: %v", err)
	}
	if d.Transport.Active() {
		t.Error("a zero transport block reports itself active")
	}
}

func TestTransportValidationRefusesWhatWouldNotWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Destination)
		want string
	}{
		{"negative packets", func(d *Destination) { d.Transport.MuxQueuePackets = -1 }, "packets out of range"},
		{"absurd packets", func(d *Destination) { d.Transport.MuxQueuePackets = MaxMuxQueuePackets + 1 }, "packets out of range"},
		{"absurd bytes", func(d *Destination) { d.Transport.MuxQueueBytes = MaxMuxQueueBytes + 1 }, "bytes out of range"},
		{"sub-second timeout", func(d *Destination) { d.Transport.RWTimeoutSeconds = -5 }, "socket timeout"},
		{"hour-plus timeout", func(d *Destination) { d.Transport.RWTimeoutSeconds = MaxRWTimeoutSeconds + 1 }, "socket timeout"},
		// The one that is a real FFmpeg semantic rather than a range: the byte
		// threshold is "the threshold after which max_muxing_queue_size is
		// taken into account", so on its own it does nothing at all. Accepting
		// it would give the operator a control that appears to work.
		{"threshold with no cap", func(d *Destination) {
			d.Transport = DestTransport{MuxQueueBytes: 1 << 20}
		}, "does nothing without a packet limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := transportDest()
			tc.mut(d)
			err := d.Validate()
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error was %q, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// A setting that is reachable in the UI and lost on save is the same class of
// bug as one that is unreachable.
func TestTransportSurvivesTheDatabaseRoundTrip(t *testing.T) {
	d := testDB(t)
	in := transportDest()
	src, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("default source: %v", err)
	}
	in.SourceID = &src

	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Transport != in.Transport {
		t.Fatalf("transport came back as %+v, want %+v", created.Transport, in.Transport)
	}

	created.Transport.RWTimeoutSeconds = 30
	created.Transport.NoDurationFilesize = false
	updated, err := d.UpdateDestination(created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Transport.RWTimeoutSeconds != 30 || updated.Transport.NoDurationFilesize {
		t.Errorf("the updated transport came back as %+v", updated.Transport)
	}

	// Clearing it must actually clear it.
	updated.Transport = DestTransport{}
	cleared, err := d.UpdateDestination(updated)
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if cleared.Transport.Active() {
		t.Errorf("the transport tuning survived being cleared: %+v", cleared.Transport)
	}
}

func TestAudioEncodingValidation(t *testing.T) {
	// The positive case first.
	d := validDest()
	d.Kind = DestSRT
	d.URL = "srt://a.example:9000"
	d.Audio = AudioEncoding{Codec: DestAudioOpus, Mono: true}
	if err := d.Validate(); err != nil {
		t.Fatalf("opus on an SRT destination was refused: %v", err)
	}

	// Opus on RTMP is refused at SAVE time rather than downgraded at start
	// time. A silent downgrade leaves the operator looking at a destination
	// whose settings say Opus and whose stream is AAC, with nothing anywhere
	// saying which is running.
	r := validDest()
	r.Kind = DestRTMP
	r.Audio = AudioEncoding{Codec: DestAudioOpus}
	err := r.Validate()
	if err == nil {
		t.Fatal("opus on an RTMP destination was accepted")
	}
	if !strings.Contains(err.Error(), "no mainstream RTMP ingest accepts it") {
		t.Errorf("error was %q; it should say WHY rather than just refusing", err)
	}

	// An unknown codec is refused rather than silently treated as AAC.
	u := validDest()
	u.Audio = AudioEncoding{Codec: "flac"}
	if err := u.Validate(); err == nil || !strings.Contains(err.Error(), "unknown audio codec") {
		t.Errorf("an unknown codec produced %v", err)
	}

	// Mono is legal everywhere, including RTMP.
	m := validDest()
	m.Kind = DestRTMP
	m.Audio = AudioEncoding{Mono: true}
	if err := m.Validate(); err != nil {
		t.Errorf("mono on RTMP was refused: %v", err)
	}
}

// Compliance must survive the round trip, and its zero value must stay zero:
// a destination that has never set any must produce no platform writes at all.
func TestComplianceSurvivesTheDatabaseRoundTrip(t *testing.T) {
	d := testDB(t)
	src, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("default source: %v", err)
	}
	no := false
	in := validDest()
	in.SourceID = &src
	in.Compliance = Compliance{
		Privacy:     PrivacyUnlisted,
		MadeForKids: &no,
		Labels:      map[string]bool{"Gambling": true, "SexualThemes": false},
	}

	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Compliance.Privacy != PrivacyUnlisted {
		t.Errorf("privacy came back as %q", created.Compliance.Privacy)
	}
	// The pointer is the whole reason "not for children" is expressible.
	if created.Compliance.MadeForKids == nil || *created.Compliance.MadeForKids {
		t.Errorf("madeForKids came back as %v, want an explicit false",
			created.Compliance.MadeForKids)
	}
	if !created.Compliance.Labels["Gambling"] || created.Compliance.Labels["SexualThemes"] {
		t.Errorf("labels came back as %v", created.Compliance.Labels)
	}

	// A destination with none must decode to a zero block rather than failing,
	// which is what a row written before the column existed looks like.
	plain := validDest()
	plain.SourceID = &src
	p2, err := d.CreateDestination(plain)
	if err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if !p2.Compliance.Empty() {
		t.Errorf("a destination with no compliance decoded to %+v", p2.Compliance)
	}
}
