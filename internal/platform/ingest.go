package platform

import (
	"strings"
	"unicode/utf16"
)

// IVSIngestHostSuffix is Amazon IVS's contribute domain, which Kick and Twitch
// both publish through. THE LEADING DOT IS LOAD-BEARING: without it
// "evilglobal-contribute.live-video.net", a different registrable domain, would
// match too.
const IVSIngestHostSuffix = ".global-contribute.live-video.net"

// IVSApp is the one RTMP application an IVS ingest accepts. Kick's dashboard
// prints the host without it, and a destination with no /app makes the stream
// key the application name, which IVS refuses by dropping the connection.
const IVSApp = "app"

// IsIVSIngestHost reports whether host -- a hostname, without a port -- is a
// subdomain of the IVS contribute domain. A host that IS the bare suffix does
// not count: there is no label in front of the dot.
func IsIVSIngestHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return len(h) > len(IVSIngestHostSuffix) && strings.HasSuffix(h, IVSIngestHostSuffix)
}

// UTF16Len is a title's length the way YouTube and the web UI's String.length
// count it: UTF-16 code units, so an emoji outside the BMP is two. Every YouTube
// title check -- the metadata API, clip publishing, recurring broadcasts -- uses
// it, so a title cannot pass one check and fail another.
func UTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }
