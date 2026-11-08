package eventsub

// Notification event bodies, one struct per shape in the subscription-types
// reference. Only the fields polyemesis reads are declared; a null decodes to
// the zero value, which is how Twitch marks an anonymous user.

// User is the user_id / user_login / user_name triple most events carry.
type User struct {
	ID    string `json:"user_id"`
	Login string `json:"user_login"`
	Name  string `json:"user_name"`
}

// FollowEvent is channel.follow v2.
type FollowEvent struct {
	User
	FollowedAt string `json:"followed_at"`
}

// SubscribeEvent is channel.subscribe v1.
type SubscribeEvent struct {
	User
	Tier   string `json:"tier"`
	IsGift bool   `json:"is_gift"`
}

// SubscriptionMessageEvent is channel.subscription.message v1.
type SubscriptionMessageEvent struct {
	User
	Tier    string `json:"tier"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	CumulativeMonths int  `json:"cumulative_months"`
	StreakMonths     *int `json:"streak_months"`
	DurationMonths   int  `json:"duration_months"`
}

// SubscriptionGiftEvent is channel.subscription.gift v1. The user fields are
// null when IsAnonymous.
type SubscriptionGiftEvent struct {
	User
	Total       int    `json:"total"`
	Tier        string `json:"tier"`
	IsAnonymous bool   `json:"is_anonymous"`
}

// CheerEvent is channel.cheer v1. The user fields are null when IsAnonymous.
type CheerEvent struct {
	User
	IsAnonymous bool   `json:"is_anonymous"`
	Message     string `json:"message"`
	Bits        int64  `json:"bits"`
}

// RaidEvent is channel.raid v1.
type RaidEvent struct {
	FromID    string `json:"from_broadcaster_user_id"`
	FromLogin string `json:"from_broadcaster_user_login"`
	FromName  string `json:"from_broadcaster_user_name"`
	ToID      string `json:"to_broadcaster_user_id"`
	ToLogin   string `json:"to_broadcaster_user_login"`
	ToName    string `json:"to_broadcaster_user_name"`
	Viewers   int    `json:"viewers"`
}

// RedemptionEvent is channel.channel_points_custom_reward_redemption.add v1.
type RedemptionEvent struct {
	ID string `json:"id"`
	User
	UserInput string `json:"user_input"`
	Status    string `json:"status"`
	Reward    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Cost  int64  `json:"cost"`
	} `json:"reward"`
	RedeemedAt string `json:"redeemed_at"`
}

// HypeTrainEvent is channel.hype_train.begin / progress / end v2.
type HypeTrainEvent struct {
	ID           string `json:"id"`
	Total        int64  `json:"total"`
	Progress     int64  `json:"progress"`
	Goal         int64  `json:"goal"`
	Level        int    `json:"level"`
	StartedAt    string `json:"started_at"`
	ExpiresAt    string `json:"expires_at"`
	EndedAt      string `json:"ended_at"`
	CooldownEnds string `json:"cooldown_ends_at"`
	Type         string `json:"type"`
}

// PollEvent is channel.poll.begin / progress / end v1.
type PollEvent struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Choices []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Votes int64  `json:"votes"`
	} `json:"choices"`
	Status    string `json:"status"` // end only: completed, archived, terminated
	StartedAt string `json:"started_at"`
	EndsAt    string `json:"ends_at"` // begin and progress
	EndedAt   string `json:"ended_at"`
}

// PredictionEvent is channel.prediction.begin / progress / lock / end v1.
type PredictionEvent struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Outcomes []struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Users         int64  `json:"users"`
		ChannelPoints int64  `json:"channel_points"`
	} `json:"outcomes"`
	Status    string `json:"status"` // end only: resolved, canceled
	StartedAt string `json:"started_at"`
	LocksAt   string `json:"locks_at"`
	LockedAt  string `json:"locked_at"`
	EndedAt   string `json:"ended_at"`
}

// StreamOnlineEvent is stream.online v1.
type StreamOnlineEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // live, playlist, watch_party, premiere, rerun
	StartedAt string `json:"started_at"`
}

// ChannelUpdateEvent is channel.update v2.
type ChannelUpdateEvent struct {
	Title        string `json:"title"`
	CategoryName string `json:"category_name"`
}

// ShoutoutCreateEvent is channel.shoutout.create v1: this channel gave one.
type ShoutoutCreateEvent struct {
	ToID        string `json:"to_broadcaster_user_id"`
	ToLogin     string `json:"to_broadcaster_user_login"`
	ToName      string `json:"to_broadcaster_user_name"`
	StartedAt   string `json:"started_at"`
	ViewerCount int    `json:"viewer_count"`
}

// ShoutoutReceiveEvent is channel.shoutout.receive v1: this channel got one.
type ShoutoutReceiveEvent struct {
	FromID      string `json:"from_broadcaster_user_id"`
	FromLogin   string `json:"from_broadcaster_user_login"`
	FromName    string `json:"from_broadcaster_user_name"`
	StartedAt   string `json:"started_at"`
	ViewerCount int    `json:"viewer_count"`
}
