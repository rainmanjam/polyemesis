package config

import (
	"os"
	"path/filepath"
	"testing"
)

// THE CONFIG BLOCK scripts/install.sh WRITES, PARSED BY THE THING THAT READS IT.
//
// install.sh now pins the FFmpeg it installed and verified, so PATH order stops
// deciding which binary polyemesis shells out to. That pin is a YAML block
// written by a bash function into a file this package loads, and nothing joined
// the two: a renamed field or a changed nesting would leave the installer
// writing a block that parses to nothing, silently, and the failure would
// surface as polyemesis using the wrong FFmpeg somewhere else entirely.
//
// Kept byte-identical to ffmpeg_yaml()'s printf so a change on either side has
// to be a change on both.
func TestTheBlockTheInstallerPinsIsTheBlockThisPackageReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	const written = `dataDir: "/var/lib/polyemesis"
addr: ":8080"
ffmpeg:
  binary: "/usr/local/bin/ffmpeg"
  probe: "/usr/local/bin/ffprobe"
`
	if err := os.WriteFile(path, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("the config install.sh writes does not load: %v", err)
	}
	if c.FFmpeg.Binary != "/usr/local/bin/ffmpeg" {
		t.Errorf("ffmpeg.binary = %q, want the pinned path — the installer's pin "+
			"is not reaching the process, so PATH decides after all", c.FFmpeg.Binary)
	}
	if c.FFmpeg.Probe != "/usr/local/bin/ffprobe" {
		t.Errorf("ffmpeg.probe = %q, want the pinned path", c.FFmpeg.Probe)
	}
}
