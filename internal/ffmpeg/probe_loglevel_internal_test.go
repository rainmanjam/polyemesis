package ffmpeg

import "testing"

func TestEncoderProbeHonoursTheLogLevelOverride(t *testing.T) {
	t.Cleanup(func() { SetLogLevel("") })
	pick := func() string {
		a := probeArgs("libx264", "")
		for i := 0; i+1 < len(a); i++ {
			if a[i] == "-loglevel" {
				return a[i+1]
			}
		}
		return ""
	}
	SetLogLevel("")
	if pick() != "warning" {
		t.Errorf("default = %q", pick())
	}
	SetLogLevel("debug")
	if pick() != "debug" {
		t.Errorf("override ignored: %q", pick())
	}
}

func TestTwoAudioProbeHonoursTheLogLevelOverride(t *testing.T) {
	t.Cleanup(func() { SetLogLevel("") })
	SetLogLevel("debug")
	a := flvProbeMuxArgs("/x.flv")
	if !(a[1] == "-loglevel" && a[2] == "debug") {
		t.Errorf("override ignored: %q", a[:4])
	}
}
