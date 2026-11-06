package api

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// THE EXPERT PREVIEW OF A DESTINATION THAT IS NOT RUNNING IS THE COMMAND THAT
// WOULD RUN. It was built from a second hand-written ffmpeg.DestSpec that
// omitted the output audio encoding and the muxer and socket tuning, so a
// destination with a transport setting was previewed WITHOUT it -- and the
// operator confirms the preview, on a screen whose whole contract is that it
// cannot drift from what runs.
func TestTheExpertPreviewOfAStoppedDestinationCarriesItsTransportTuning(t *testing.T) {
	s, _, _, _ := engineServer(t, defaultTools(), Options{})
	row := &db.Destination{
		ID: 1, Name: "twitch", Kind: db.DestRTMP, URL: "rtmp://live.example/app", StreamKey: "k",
		Profile: routing.DefaultProfile(),
		Transport: db.DestTransport{
			NoDurationFilesize: true, RWTimeoutSeconds: 10, MuxQueuePackets: 4096,
		},
	}
	_, base, live, _, err := s.destinationBaseArgv(row)
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("the destination is not running, so the command is a rebuild")
	}
	for _, want := range [][]string{
		{"-flvflags", "no_duration_filesize"},
		{"-rw_timeout", "10000000"},
		{"-max_muxing_queue_size", "4096"},
	} {
		if !containsPair(base, want[0], want[1]) {
			t.Errorf("the preview omits %v, which the running command carries: %q", want, base)
		}
	}
}

func containsPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}
