package playlistmedia

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the playlist normalise kind.
var Info = jobs.KindInfo{
	Kind:        KindNormalise,
	Label:       "Playlist normalise",
	Description: "Transcodes one uploaded playlist item to the single fixed profile every item must share. A playlist will not go on air until every one of its items has been through this.",
}
