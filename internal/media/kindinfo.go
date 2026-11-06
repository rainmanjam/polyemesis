package media

import "github.com/rainmanjam/polyemesis/internal/jobs"

// ProxyInfo, ThumbnailsInfo and ArchiveInfo are the jobs-page entries for the
// kinds Register wires.
var ProxyInfo = jobs.KindInfo{
	Kind:        KindProxy,
	Label:       "Proxy encode",
	Description: "A small H.264/MP4 copy a browser can actually play. The MKV masters cannot be played in a browser; this is what the library's inline player uses.",
}

var ThumbnailsInfo = jobs.KindInfo{
	Kind:        KindThumbnails,
	Label:       "Thumbnails",
	Description: "Poster frame, contact sheet and the scrub sprites the timeline shows on hover.",
}

var ArchiveInfo = jobs.KindInfo{
	Kind:        KindArchive,
	Label:       "Archive compression",
	Description: "Re-encodes an old recording to HEVC to reclaim disk. Lossy, verified after the fact, and never applied to a recording younger than the configured age.",
}
