package platform

// YouTube Data API quota, in units. One allowance covers chat polling, title
// pushes, poll mirroring, moderation and clip uploads, which is why the costs
// have one home: every package that spends from it must agree on the price.
const (
	YouTubeCostList = 1
	// YouTubeCostWrite is every insert, update, delete, bind, transition,
	// thumbnails.set and playlistItems.insert the Data API charges at 50.
	YouTubeCostWrite = 50
	// YouTubeCostUploadVideo is videos.insert.
	YouTubeCostUploadVideo = 1600

	YouTubeCostListMessages = 5
	// YouTubeCostListBroadcasts is UNVERIFIED; see internal/chat/quota.go.
	YouTubeCostListBroadcasts  = 1
	YouTubeCostListLiveStreams = 1
	YouTubeCostSendMessage     = 50
	YouTubeCostDeleteMessage   = 50
	YouTubeCostBan             = 50
	YouTubeCostPollCreate      = 20
	YouTubeCostPollClose       = 20

	// YouTubeDefaultDailyUnits is a Google Cloud project's default daily
	// allowance, which resets at midnight Pacific.
	YouTubeDefaultDailyUnits = 10000
)
