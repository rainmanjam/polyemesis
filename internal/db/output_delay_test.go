package db

import "testing"

// TestAYouTubePOSTCaptionDestinationsDelayIsYouTubesAndNotPolyemesiss: a YouTube
// destination that posts captions by HTTP POST keeps its 30 or 60 second delay
// as the broadcast's broadcastStreamDelayMs, and YouTube holds the viewers back
// by it. polyemesis does not hold that destination's output back as well, which
// would put viewers twice the delay behind. Every other destination is held
// back locally by exactly its DelaySeconds, as before.
//
// MUTATION: OutputDelaySeconds returning DelaySeconds for every row. Observed:
// FAIL, the youtube_post row was held back 30 s locally.
func TestAYouTubePOSTCaptionDestinationsDelayIsYouTubesAndNotPolyemesiss(t *testing.T) {
	cases := []struct {
		name string
		d    Destination
		want int
	}{
		{"youtube_post on YouTube", Destination{Platform: PlatformYouTube, CaptionMode: "youtube_post", DelaySeconds: 30}, 0},
		{"youtube_post on YouTube, 60 s", Destination{Platform: PlatformYouTube, CaptionMode: "youtube_post", DelaySeconds: 60}, 0},
		{"embedded captions on YouTube", Destination{Platform: PlatformYouTube, CaptionMode: "sei", DelaySeconds: 30}, 30},
		{"no captions on YouTube", Destination{Platform: PlatformYouTube, DelaySeconds: 45}, 45},
		{"a delayed Twitch destination", Destination{Platform: PlatformTwitch, DelaySeconds: 20}, 20},
		{"youtube_post left on a row that is no longer YouTube", Destination{Platform: PlatformTwitch, CaptionMode: "youtube_post", DelaySeconds: 30}, 30},
		{"no delay", Destination{Platform: PlatformYouTube, CaptionMode: "youtube_post"}, 0},
	}
	for _, c := range cases {
		if got := c.d.OutputDelaySeconds(); got != c.want {
			t.Errorf("%s: OutputDelaySeconds = %d, want %d", c.name, got, c.want)
		}
	}
	var nilDest *Destination
	if got := nilDest.OutputDelaySeconds(); got != 0 {
		t.Errorf("a nil destination is held back %d s, want 0", got)
	}
}
