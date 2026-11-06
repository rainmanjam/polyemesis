package api

import (
	"fmt"
	"strings"
)

// The routes whose paths the server also hands out -- to a browser, to an
// operator, to a client reading JSON. Each pattern is declared here, registered
// from here, and every URL built for it is built here, so the registration and
// the copy cannot drift apart. All are wire values: never rename.
const (
	apiBase = "/api/v1"

	routeOAuthStart    = "/oauth/{platform}/start"
	routeOAuthCallback = "/oauth/{platform}/callback"
	routeKickWebhook   = "/chat/kick/{secret}"
	routeLibraryMedia  = "/library/recordings/{id}/media/{file}"

	// settingsPlatformsPath is the SPA route an OAuth round trip returns to;
	// ui/src/pages/SettingsPage.tsx reads the three query keys.
	settingsPlatformsPath = "/settings?tab=platforms"
	oauthOKKey            = "oauth_ok"
	oauthErrorKey         = "oauth_error"
	oauthWarnKey          = "oauth_warn"
)

// oauthStartPath is the URL that starts a platform's OAuth connect.
func oauthStartPath(platform string) string {
	return apiBase + strings.Replace(routeOAuthStart, "{platform}", platform, 1)
}

// kickWebhookPath is the path Kick's webhook posts to; the secret is the
// credential.
func kickWebhookPath(secret string) string {
	return apiBase + strings.Replace(routeKickWebhook, "{secret}", secret, 1)
}

// libraryMediaBase is the URL prefix a recording's media files resolve under.
func libraryMediaBase(recordingID int64) string {
	base := apiBase + routeLibraryMedia
	base = strings.Replace(base, "{id}", fmt.Sprint(recordingID), 1)
	return strings.TrimSuffix(base, "{file}")
}

// settingsRedirect is where the browser lands after an OAuth round trip, with
// the outcome as query values (already escaped), in the order given.
func settingsRedirect(pairs ...[2]string) string {
	target := settingsPlatformsPath
	for _, p := range pairs {
		target += "&" + p[0] + "=" + p[1]
	}
	return target
}
