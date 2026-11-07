package platform

// YouTube Data API quota, in units. One allowance covers chat polling, title
// pushes, poll mirroring and moderation, which is why the costs have one home:
// every package that spends from it must agree on the price.
//
// The prices are from Google's quota table,
// https://developers.google.com/youtube/v3/determine_quota_cost (read
// 2026-11-07). Where the table and an older figure here disagreed the table won:
//
//	liveBroadcasts and liveStreams  every method        1
//	liveChatMessages                insert, delete,
//	                                transition           20
//	liveChatMessages                list, streamList      5
//	liveChatBans                    insert, delete      200
//	liveChatModerators              insert, delete      200
//	liveChatModerators              list                 50
//	videos                          insert                1, in its OWN bucket
//	                                                      (100 calls a day)
//
// The Live Streaming calls used to be priced at 50 as "writes", and chat send,
// delete and ban at 50, and videos.insert at 1,600. None of that is in the
// table any more.
const (
	YouTubeCostList = 1
	// YouTubeCostWrite is every insert, update, delete, thumbnails.set and
	// playlistItems.insert on the ordinary resources, which the table prices at
	// 50. It does NOT cover the Live Streaming resources (YouTubeCostLiveWrite)
	// or live chat (the per-call constants below).
	YouTubeCostWrite = 50
	// YouTubeCostLiveWrite is every liveBroadcasts and liveStreams method
	// (bind, insert, update, delete, transition, insertCuepoint): 1.
	YouTubeCostLiveWrite = 1
	// YouTubeCostUploadVideo is videos.insert: 1 unit, charged to its OWN bucket
	// of YouTubeUploadDailyCalls a day rather than to the shared allowance, so
	// the shared meter does not count it.
	YouTubeCostUploadVideo = 1
	// YouTubeUploadDailyCalls is that bucket's default size, in calls.
	YouTubeUploadDailyCalls = 100

	YouTubeCostListMessages = 5
	// YouTubeCostListBroadcasts is liveBroadcasts.list; see internal/chat/quota.go.
	YouTubeCostListBroadcasts  = 1
	YouTubeCostListLiveStreams = 1
	YouTubeCostSendMessage     = 20
	YouTubeCostDeleteMessage   = 20
	YouTubeCostBan             = 200
	// liveChatBans.delete is also 200; liveChatModerators.insert and .delete
	// are 200 and .list is 50.
	YouTubeCostUnban         = 200
	YouTubeCostModeratorList = 50
	YouTubeCostModeratorEdit = 200
	YouTubeCostPollCreate    = 20
	YouTubeCostPollClose     = 20

	// YouTubeDefaultDailyUnits is a Google Cloud project's default daily
	// allowance, which resets at midnight Pacific.
	YouTubeDefaultDailyUnits = 10000
)
