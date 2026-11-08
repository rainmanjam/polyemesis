// Package eventsub is the client half of Twitch EventSub over WebSocket: the
// message envelope, the close codes, the catalogue of subscription types with
// the scopes and tiers they need, the Helix calls that create subscriptions
// and read the channel's tier, and a connection that frames the socket.
//
// It decodes and frames; it does not decide what an event means. The mapping
// to viewer activity is internal/activityhub/twitch.go, which keeps this
// package free of the activity model's rules and reusable for a later
// chat-over-EventSub transport.
//
// Everything here is written against Twitch's current reference:
//
//	https://dev.twitch.tv/docs/eventsub/websocket-reference
//	https://dev.twitch.tv/docs/eventsub/handling-websocket-events
//	https://dev.twitch.tv/docs/eventsub/eventsub-subscription-types
//	https://dev.twitch.tv/docs/eventsub/manage-subscriptions
//
// A WebSocket carries no signature (the HMAC is webhook-only): the connection's
// authenticity is TLS plus the user token that created its subscriptions.
package eventsub
