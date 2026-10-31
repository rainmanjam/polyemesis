package testenv

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// THE FFMPEG install.sh PUTS ON A HOST IS ONE BUILD, NAMED BY DATE AND HASH.
//
// offer_ffmpeg_upgrade used to download BtbN's `latest` release -- a tag BtbN
// moves to a new build every day -- and check it against the checksums.sha256
// published in that same release. Two consequences, both invisible from the
// script:
//
//   - Two staging installs a day apart got two different FFmpeg builds, under
//     one asset name, with nothing on either host recording which.
//   - The integrity check could only catch a corrupted download. Whoever could
//     replace the tarball could replace the checksum file beside it, and the
//     result was then extracted and EXECUTED AS ROOT to probe for libsrt.
//
// Pinning a dated autobuild tag and hard-coding each asset's sha256 in the
// script fixes both: the hash now comes from this repository, reviewed in a
// pull request, not from the server that serves the file. Staging-readiness
// row 11. This reads the script because Go cannot run it on every CI platform;
// the rung is the control inside install.sh, and this keeps it from being
// quietly reverted to `latest`.
func TestInstallerPinsOneDatedFFmpegBuildByHash(t *testing.T) {
	src := installerSource(t)

	if strings.Contains(src, "FFmpeg-Builds/releases/download/latest") {
		t.Error("install.sh downloads from BtbN's rolling `latest` release. That tag " +
			"moves daily, so two installs get two builds; pin FFMPEG_BTBN_TAG instead.")
	}

	tag := regexp.MustCompile(`(?m)^FFMPEG_BTBN_TAG=(\S+)$`).FindStringSubmatch(src)
	if tag == nil {
		t.Fatal("install.sh has no top-level FFMPEG_BTBN_TAG=<tag> assignment")
	}
	if !regexp.MustCompile(`^autobuild-\d{4}-\d{2}-\d{2}-\d{2}-\d{2}$`).MatchString(tag[1]) {
		t.Errorf("FFMPEG_BTBN_TAG=%s is not a dated BtbN autobuild tag "+
			"(autobuild-YYYY-MM-DD-HH-MM); anything else can move", tag[1])
	}

	assets := functionBody(t, src, "ffmpeg_static_asset")
	if strings.Contains(assets, "latest") {
		t.Error("ffmpeg_static_asset still names a `-latest-` asset. Those exist only " +
			"in the rolling release; a dated release names the exact version (n8.1.3).")
	}

	// One hash per architecture, each a full sha256, and not the same one
	// twice -- a copy-paste of amd64's hash into arm64 would refuse every arm64
	// install and read like BtbN had been tampered with.
	sums := functionBody(t, src, "ffmpeg_static_sha256")
	hexRe := regexp.MustCompile(`(?m)^\s*(amd64|arm64)\)\s+echo\s+"?([0-9a-f]{64})"?\s*;;`)
	got := map[string]string{}
	for _, m := range hexRe.FindAllStringSubmatch(sums, -1) {
		got[m[1]] = m[2]
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if got[arch] == "" {
			t.Errorf("ffmpeg_static_sha256 has no 64-hex sha256 for %s", arch)
		}
	}
	if got["amd64"] != "" && got["amd64"] == got["arm64"] {
		t.Error("ffmpeg_static_sha256 gives amd64 and arm64 the same hash")
	}

	body := functionBody(t, src, "offer_ffmpeg_upgrade")
	if !strings.Contains(body, "releases/download/${FFMPEG_BTBN_TAG}/") {
		t.Error("offer_ffmpeg_upgrade does not download from the pinned FFMPEG_BTBN_TAG")
	}
	// The same-origin checksum file is exactly what the pin replaces: a hash
	// fetched from the server that serves the tarball proves nothing about it.
	if strings.Contains(body, "checksums.sha256") {
		t.Error("offer_ffmpeg_upgrade still trusts BtbN's checksums.sha256 from the " +
			"same release as the tarball; verify against ffmpeg_static_sha256")
	}
	verify := strings.Index(body, "ffmpeg_static_sha256")
	extract := strings.Index(body, "tar xf")
	run := strings.Index(body, `"$tmp/x/bin/ffmpeg"`)
	if verify < 0 {
		t.Fatal("offer_ffmpeg_upgrade never checks the download against ffmpeg_static_sha256")
	}
	if extract >= 0 && extract < verify {
		t.Error("offer_ffmpeg_upgrade extracts the archive before checking its pinned hash")
	}
	if run >= 0 && run < verify {
		t.Error("offer_ffmpeg_upgrade runs the downloaded ffmpeg before checking its pinned hash")
	}
}

// THE PINNED TAG HAS TO BE ONE BtbN KEEPS, AND ONLY THE MONTH-END ONES ARE.
//
// The pin above is only as durable as the release it names. The comment beside
// it in install.sh said BtbN keeps dated releases "for roughly two years", and
// that is true of exactly one release per month: the last build of each month.
// The dailies in between are deleted after about two weeks. On 2026-10-29 the
// list ran autobuild-2024-11-30, 2024-12-31, ... 2026-09-30 (month-ends only),
// then 2026-10-16 onward (the recent dailies). The pinned
// autobuild-2026-09-23-14-55 was a daily, it had been deleted, and every
// install.sh --ffmpeg force was a 404 that read like a CDN outage.
//
// So the date in the tag must be the last day of its month. That is a property
// of the string, which is all this test can see, and it is the one that decides
// whether the download still exists next year.
func TestInstallerPinsAMonthEndFFmpegBuildBtbNKeeps(t *testing.T) {
	src := installerSource(t)
	tag := regexp.MustCompile(`(?m)^FFMPEG_BTBN_TAG=autobuild-(\d{4})-(\d{2})-(\d{2})-\d{2}-\d{2}$`).FindStringSubmatch(src)
	if tag == nil {
		t.Fatal("install.sh has no FFMPEG_BTBN_TAG=autobuild-YYYY-MM-DD-HH-MM assignment")
	}
	day, err := time.Parse("2006-01-02", tag[1]+"-"+tag[2]+"-"+tag[3])
	if err != nil {
		t.Fatalf("FFMPEG_BTBN_TAG date %s-%s-%s does not parse: %v", tag[1], tag[2], tag[3], err)
	}
	if next := day.AddDate(0, 0, 1); next.Month() == day.Month() {
		t.Errorf("FFMPEG_BTBN_TAG is dated %s, which is not the last day of its month. "+
			"BtbN deletes its daily autobuilds after about two weeks and keeps only the "+
			"last build of each month, so this pin will 404. Pick the month-end release "+
			"(e.g. autobuild-%s-%s-<last day>-HH-MM) from "+
			"https://github.com/BtbN/FFmpeg-Builds/releases", day.Format("2006-01-02"), tag[1], tag[2])
	}

	// A dated release names its assets with the git describe of the build,
	// n<version>-<commits>-g<hash>, or the bare n<version> when it sat on the
	// tag. Either way the version in the name has to be the line the asset
	// belongs to, or the hash pins a file nobody meant.
	assets := functionBody(t, src, "ffmpeg_static_asset")
	nameRe := regexp.MustCompile(`ffmpeg-n(\d+)\.(\d+)(?:\.\d+)?(?:-\d+-g[0-9a-f]+)?-linux(?:arm)?64-gpl-(\d+)\.(\d+)\.tar\.xz`)
	names := nameRe.FindAllStringSubmatch(assets, -1)
	if len(names) != 2 {
		t.Fatalf("ffmpeg_static_asset names %d dated BtbN GPL tarballs, want 2 (amd64, arm64)", len(names))
	}
	for _, m := range names {
		if m[1] != m[3] || m[2] != m[4] {
			t.Errorf("%s: the version in the name is n%s.%s but the series suffix is %s.%s",
				m[0], m[1], m[2], m[3], m[4])
		}
	}
}
