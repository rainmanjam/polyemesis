package media

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The unit tests prove the argument builders produce the strings we meant. This
// file proves the strings mean what we think they mean to a real FFmpeg — which
// is a different claim, and the one that actually protects the product. A
// filter graph that does not configure, a -movflags that silently does nothing,
// a sprite grid whose tiles are not the size the WebVTT says: every one of them
// passes a string comparison and fails a viewer.
//
// Skipped without FFmpeg, and in -short: it encodes a real file.

// syntheticMaster writes a short three-track Matroska recording shaped like the
// thing this product records — one video track and three separate microphones,
// two of them stereo and one mono, each with its own title.
func syntheticMaster(t *testing.T, ffmpegBin, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "rec-20240115-143000.mkv")
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=15:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=20",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=20",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "30", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac:a:0", "2", "-ac:a:1", "1", "-ac:a:2", "2",
		"-metadata:s:a:0", "title=Host", "-metadata:s:a:0", "language=eng",
		"-metadata:s:a:1", "title=Guest", "-metadata:s:a:1", "language=eng",
		"-metadata:s:a:2", "title=Music",
		"-f", "matroska", path,
	}
	if out, err := exec.Command(ffmpegBin, args...).CombinedOutput(); err != nil {
		t.Fatalf("building the synthetic master: %v\n%s", err, out)
	}
	return path
}

func tools(t *testing.T) (ffmpegBin, ffprobeBin string) {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes a real file")
	}
	var err error
	if ffmpegBin, err = exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if ffprobeBin, err = exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	return ffmpegBin, ffprobeBin
}

func runFFmpeg(t *testing.T, bin string, args []string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s\nfailed: %v\n%s", bin, strings.Join(args, " "), err, stderr.String())
	}
}

func probe(t *testing.T, ffprobeBin, path string) FileSummary {
	t.Helper()
	out, err := exec.Command(ffprobeBin, ProbeArgs(path)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	s, err := ParseSummary(out, path, size)
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------- proxy

func TestRealProxyIsPlayableAndHasItsIndexAtTheFront(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)
	out := filepath.Join(dir, "proxy.mp4")

	runFFmpeg(t, ffmpegBin, ProxyArgs(ProxySpec{Input: master, Output: out, AudioTrack: 1}))

	// +faststart moves the moov atom ahead of the media data. Without it a
	// browser downloads the whole file before it can show a frame, and the
	// scrub bar the proxy exists for does not work. Verified by reading the
	// box order, because the flag failing silently is exactly the risk.
	head := make([]byte, 4096)
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, _ := f.Read(head)
	head = head[:n]
	moov, mdat := bytes.Index(head, []byte("moov")), bytes.Index(head, []byte("mdat"))
	if moov < 0 {
		t.Fatal("no moov atom in the first 4 KiB: the proxy is not faststart")
	}
	if mdat >= 0 && mdat < moov {
		t.Fatal("mdat precedes moov: +faststart did nothing")
	}

	got := probe(t, ffprobeBin, out)
	if got.VideoCodec != "h264" {
		t.Fatalf("proxy video codec = %q", got.VideoCodec)
	}
	// One track, and specifically the one that was asked for: the proxy is for
	// navigation, and a browser cannot choose between six.
	if len(got.Audio) != 1 {
		t.Fatalf("proxy carries %d audio tracks, want 1", len(got.Audio))
	}
	if got.Audio[0].Codec != "aac" || got.Audio[0].Channels != 2 {
		t.Fatalf("proxy audio = %+v, want stereo aac", got.Audio[0])
	}
	if got.DurationSeconds < 19 || got.DurationSeconds > 21 {
		t.Fatalf("proxy is %.2fs long, want about 20", got.DurationSeconds)
	}
	// The point of the whole exercise: much smaller than the master.
	src := probe(t, ffprobeBin, master)
	if got.Bytes >= src.Bytes {
		t.Fatalf("proxy is %d bytes and the master is %d", got.Bytes, src.Bytes)
	}
}

func TestRealProxySurvivesAnAudioTrackThatIsNotThere(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)
	out := filepath.Join(dir, "proxy.mp4")

	// Track 9 does not exist. The optional '?' on the map is what turns this
	// from a failed job into a silent proxy.
	runFFmpeg(t, ffmpegBin, ProxyArgs(ProxySpec{Input: master, Output: out, AudioTrack: 9}))

	got := probe(t, ffprobeBin, out)
	if got.VideoCodec == "" {
		t.Fatal("the proxy lost its video when the audio track was missing")
	}
	if len(got.Audio) != 0 {
		t.Fatalf("a missing track produced %d audio tracks", len(got.Audio))
	}
}

// ----------------------------------------------------------------- thumbnails

func TestRealThumbnailPassProducesEveryArtefactFromOneDecode(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)

	spec := ThumbnailSpec{
		Input:           master,
		DurationSeconds: 20,
		Poster:          PosterSpec{Output: filepath.Join(dir, PosterName)},
		ContactSheet:    ContactSheetSpec{Output: filepath.Join(dir, ContactSheetName), Cols: 3, Rows: 2},
		Sprites: SpriteSpec{
			OutputPattern: filepath.Join(dir, SpritePattern),
			// 20s at 2s spacing over a 2x2 grid is five thumbnails across two
			// sheets, so the sheet roll-over is exercised rather than assumed.
			IntervalSeconds: 2,
			Cols:            2, Rows: 2,
			TileWidth: 160, TileHeight: 90,
		},
	}
	runFFmpeg(t, ffmpegBin, ThumbnailArgs(spec))

	poster := probe(t, ffprobeBin, spec.Poster.Output)
	if poster.VideoCodec != "mjpeg" {
		t.Fatalf("poster codec = %q", poster.VideoCodec)
	}

	// The contact sheet's geometry has to be the grid we asked for, or the
	// "whole recording at a glance" claim is a lie about which moments it shows.
	sheet := probeSize(t, ffprobeBin, spec.ContactSheet.Output)
	cs := spec.ContactSheet.Normalized()
	wantW := cs.Cols*cs.TileWidth + 2*cs.Margin + (cs.Cols-1)*cs.Padding
	wantH := cs.Rows*cs.TileHeight + 2*cs.Margin + (cs.Rows-1)*cs.Padding
	if sheet.w != wantW || sheet.h != wantH {
		t.Fatalf("contact sheet is %dx%d, want %dx%d", sheet.w, sheet.h, wantW, wantH)
	}

	// The sprite claim is the strongest one in this package: the WebVTT
	// addresses each thumbnail by pixel rectangle, so the sheets FFmpeg wrote
	// must be exactly the size the arithmetic predicted, and there must be as
	// many of them as the VTT names.
	sprites := spec.Normalized().Sprites
	for i := 1; i <= sprites.Sheets(); i++ {
		name := filepath.Join(dir, SheetName(sprites.OutputPattern, i))
		size := probeSize(t, ffprobeBin, name)
		if size.w != sprites.Cols*sprites.TileWidth || size.h != sprites.Rows*sprites.TileHeight {
			t.Fatalf("sprite sheet %d is %dx%d, want %dx%d — every WebVTT rectangle after the first would be wrong",
				i, size.w, size.h, sprites.Cols*sprites.TileWidth, sprites.Rows*sprites.TileHeight)
		}
	}
	// And FFmpeg wrote no sheet the VTT does not know about.
	extra := filepath.Join(dir, SheetName(sprites.OutputPattern, sprites.Sheets()+1))
	if _, err := os.Stat(extra); err == nil {
		t.Fatalf("FFmpeg wrote %s, which the WebVTT never names", filepath.Base(extra))
	}

	vtt := sprites.VTT()
	if n := strings.Count(vtt, "-->"); n != sprites.Frames() {
		t.Fatalf("the VTT has %d cues for %d thumbnails", n, sprites.Frames())
	}
}

type imageSize struct{ w, h int }

func probeSize(t *testing.T, ffprobeBin, path string) imageSize {
	t.Helper()
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var s imageSize
	fields := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(fields) != 2 {
		t.Fatalf("unreadable size %q for %s", out, path)
	}
	for i, f := range fields {
		var v int
		for _, r := range f {
			if r < '0' || r > '9' {
				t.Fatalf("unreadable size %q for %s", out, path)
			}
			v = v*10 + int(r-'0')
		}
		if i == 0 {
			s.w = v
		} else {
			s.h = v
		}
	}
	return s
}

func TestRealPosterAndContactSheetWorkOnTheirOwnToo(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)

	poster := filepath.Join(dir, PosterName)
	runFFmpeg(t, ffmpegBin, PosterArgs(PosterSpec{Input: master, Output: poster, DurationSeconds: 20}))
	if probeSize(t, ffprobeBin, poster).h != DefaultPosterHeight {
		t.Fatalf("poster is not %dp", DefaultPosterHeight)
	}

	contact := filepath.Join(dir, ContactSheetName)
	runFFmpeg(t, ffmpegBin, ContactSheetArgs(ContactSheetSpec{
		Input: master, Output: contact, DurationSeconds: 20, Cols: 2, Rows: 2}))
	if _, err := os.Stat(contact); err != nil {
		t.Fatalf("no contact sheet: %v", err)
	}
}

// A source whose aspect does not match the tile is the case that would slide
// every WebVTT rectangle sideways if the pad were ever dropped.
func TestRealSpriteTilesAreExactEvenWhenTheSourceAspectDisagrees(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	// 4:3 source, 16:9 tiles.
	master := syntheticMaster(t, ffmpegBin, dir)

	spec := SpriteSpec{
		Input:           master,
		OutputPattern:   filepath.Join(dir, SpritePattern),
		DurationSeconds: 20,
		IntervalSeconds: 5,
		Cols:            2, Rows: 2,
		TileWidth: 160, TileHeight: 90,
	}
	runFFmpeg(t, ffmpegBin, SpriteArgs(spec))

	size := probeSize(t, ffprobeBin, filepath.Join(dir, SheetName(spec.OutputPattern, 1)))
	if size.w != 320 || size.h != 180 {
		t.Fatalf("sheet is %dx%d, want 320x180", size.w, size.h)
	}
}

// ------------------------------------------------------------------- archive

func TestRealArchiveKeepsEveryAudioTrackAndVerifiesAgainstTheOriginal(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)
	out := filepath.Join(dir, "archive.mkv")

	if !encoderAvailable(ffmpegBin, "libx265") {
		t.Skip("this build has no libx265")
	}
	// ultrafast keeps the test short; the encoder choice is what is under test,
	// not the quality it reaches.
	runFFmpeg(t, ffmpegBin, ArchiveArgs(ArchiveSpec{
		Input: master, Output: out, Codec: ArchiveHEVC, Preset: "ultrafast", Quality: 30}))

	src := probe(t, ffprobeBin, master)
	got := probe(t, ffprobeBin, out)

	if got.VideoCodec != "hevc" {
		t.Fatalf("archive video codec = %q, want hevc", got.VideoCodec)
	}
	// THE guarantee. Three microphones in, three microphones out, with their
	// channel counts and their labels.
	if len(got.Audio) != 3 {
		t.Fatalf("the archive has %d audio tracks, want 3 — the multitrack master would be destroyed", len(got.Audio))
	}
	for i, want := range src.Audio {
		if got.Audio[i] != want {
			t.Fatalf("audio track %d = %+v, want %+v", i, got.Audio[i], want)
		}
	}

	// And the verifier agrees, using the real numbers rather than fixtures.
	v := VerifyArchive(src, got, decodeCheck(t, ffmpegBin, out), VerifyOptions{AllowLarger: true})
	if !v.OK {
		t.Fatalf("the verifier refused a good archive: %v", v.Reasons)
	}
}

// decodeCheck runs the real decode pass through the real Execer, which is the
// point: the stream split matters. FFmpeg's -progress block goes to stdout and
// its complaints to stderr, and a test that merged the two would report
// "speed= 654x" as a decode error — as this one did before it used Exec.
func decodeCheck(t *testing.T, ffmpegBin, path string) []string {
	t.Helper()
	var lines []string
	err := Exec(context.Background(), Command{Name: ffmpegBin, Args: DecodeCheckArgs(path)},
		Sink{Line: func(l string) { lines = append(lines, l) }})
	got := DecodeErrors(strings.Join(lines, "\n"))
	if err != nil && len(got) == 0 {
		got = []string{err.Error()}
	}
	return got
}

func encoderAvailable(ffmpegBin, name string) bool {
	out, err := exec.Command(ffmpegBin, "-hide_banner", "-encoders").Output()
	if err != nil {
		return true // assume the best; the encode itself will say otherwise
	}
	for _, f := range strings.Fields(string(out)) {
		if f == name {
			return true
		}
	}
	return false
}

// The decode check has to look at every stream. A corrupt fourth track that a
// default null pass never decodes is precisely the damage this gate exists to
// catch, and it would be caught after the original was already gone.
func TestRealDecodeCheckIsSilentOnAHealthyFileAndLoudOnADamagedOne(t *testing.T) {
	ffmpegBin, _ := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)

	if errs := decodeCheck(t, ffmpegBin, master); len(errs) != 0 {
		t.Fatalf("a healthy file was reported as damaged: %v", errs)
	}

	// Corrupt the middle of the file, well past the header, so the container
	// still opens and only a decode notices.
	damaged := filepath.Join(dir, "damaged.mkv")
	raw, err := os.ReadFile(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(raw) / 3; i < len(raw)*2/3; i++ {
		raw[i] ^= 0xff
	}
	if err := os.WriteFile(damaged, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if errs := decodeCheck(t, ffmpegBin, damaged); len(errs) == 0 {
		t.Fatal("a file with a third of its bytes flipped decoded cleanly")
	}
}

// End to end through the worker, with a real FFmpeg and a real ffprobe behind
// it. Everything above tests one command; this tests that the worker composes
// them into a thing that keeps its promise.
func TestRealArchiveWorkerRefusesToReplaceAnOriginalItCannotVerify(t *testing.T) {
	ffmpegBin, ffprobeBin := tools(t)
	dir := t.TempDir()
	master := syntheticMaster(t, ffmpegBin, dir)
	before, err := os.ReadFile(master)
	if err != nil {
		t.Fatal(err)
	}

	proc := New(testLog(), Config{
		FFmpeg: ffmpegBin, FFprobe: ffprobeBin, RecordingsDir: dir,
		ArchiveEnabled: true, ArchiveAllowReplace: true,
	})
	// A source summary that claims a fourth audio track the archive can never
	// produce, which is the shape of every "we lost a microphone" failure.
	realProbe := proc.probe
	proc.probe = func(ctx context.Context, path string) (FileSummary, error) {
		s, err := realProbe(ctx, path)
		if err != nil || strings.Contains(path, ArchiveBase) {
			return s, err
		}
		s.Audio = append(s.Audio, TrackSummary{Index: 3, Codec: "aac", Channels: 2, Title: "Phantom"})
		return s, nil
	}

	// Quality 28 rather than the 34 this test used to reach for to go faster:
	// with ReplaceOriginal set, 34 is now refused before the encode starts, and
	// it is worth noticing that the number was picked here for speed by someone
	// who was not thinking about the picture at all — which is the whole
	// mechanism of the mistake the bound exists to stop. The speed comes from
	// the ultrafast preset, which costs nothing anybody cares about.
	job := mustJob(NewArchiveJob(1, ArchiveParams{
		Recording: filepath.Base(master), DurationMS: 20000,
		RecordedAtUnix: 1, AcknowledgeLossy: true, ReplaceOriginal: true,
		Preset: "ultrafast", Quality: 28,
	}))
	// RecordedAtUnix 1 is 1970, comfortably past any archive age.
	err = proc.RunArchive(context.Background(), job, &fakeReporter{})
	if err == nil {
		t.Fatal("the worker replaced an original whose copy lost a track")
	}

	after, readErr := os.ReadFile(master)
	if readErr != nil {
		t.Fatalf("THE ORIGINAL IS GONE: %v", readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("THE ORIGINAL WAS MODIFIED")
	}
	if _, err := os.Stat(LayoutFor(dir, filepath.Base(master)).Archive); !os.IsNotExist(err) {
		t.Fatal("the unverified copy was published anyway")
	}
}
