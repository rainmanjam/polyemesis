package api

import "github.com/rainmanjam/polyemesis/internal/chat"

// The X adapter reports its activity kinds to GET /api/v1/activity/sources through
// this capability; losing the method would silently drop X from the list.
var _ activityKindReporter = (*chat.XAdapter)(nil)
