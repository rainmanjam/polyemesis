package chat

import (
	"github.com/rainmanjam/polyemesis/internal/db"
)

// Profile is what one platform's adapter can do, read off the adapter's TYPE.
// What decides whether Hub.Ban works is whether the adapter satisfies Banner, so
// the answer is a property of the type and needs no credentials or live
// adapter. internal/platform cannot ask chat (it is a leaf and chat imports it),
// so chat publishes this and TestRegistryAgreesWithTheChatAdapters holds the
// registry's description of the same platforms to it.
type Profile struct {
	Platform db.Platform
	// Send is whether the adapter implements Sender, and SendMax the limit it
	// enforces in runes (zero when it cannot send).
	Send    bool
	SendMax int
	Delete  bool
	// Ban covers timeout and permanent ban, which both route through Banner.
	Ban bool
	// NoTimeout is a Banner that cannot time anyone out (Facebook): Ban is true
	// and only the permanent form works.
	NoTimeout bool
	Hide      bool
}

// adapterTypes is one nil-valued pointer per platform adapter, purely so the
// interface assertions have something to assert against.
var adapterTypes = map[db.Platform]any{
	db.PlatformKick:     (*KickAdapter)(nil),
	db.PlatformTwitch:   (*TwitchAdapter)(nil),
	db.PlatformYouTube:  (*YouTubeAdapter)(nil),
	db.PlatformFacebook: (*FacebookAdapter)(nil),
	// X has Deleter (DELETE .../chat/{id}) and Banner (mute and unmute). It has
	// no Hider, because X offers no hide action.
	db.PlatformX:      (*XAdapter)(nil),
	db.PlatformRumble: (*RumbleAdapter)(nil),
}

// sendLimits is each sending adapter's enforced maximum.
var sendLimits = map[db.Platform]int{
	db.PlatformKick:    KickMaxMessage,
	db.PlatformTwitch:  TwitchMaxMessage,
	db.PlatformYouTube: YouTubeMaxMessage,
	db.PlatformX:       XMaxMessage,
}

// ProfileOf is the profile of one platform's adapter, and false for a platform
// chat has no adapter for.
func ProfileOf(p db.Platform) (Profile, bool) {
	ad, ok := adapterTypes[p]
	if !ok {
		return Profile{}, false
	}
	_, send := ad.(Sender)
	_, del := ad.(Deleter)
	_, ban := ad.(Banner)
	_, hide := ad.(Hider)
	_, noTimeout := ad.(NoTimeouter)
	pr := Profile{Platform: p, Send: send, Delete: del, Ban: ban, NoTimeout: noTimeout, Hide: hide}
	if send {
		pr.SendMax = sendLimits[p]
	}
	return pr, true
}
