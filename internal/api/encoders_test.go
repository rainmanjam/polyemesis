package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// encoderChoice mirrors encoderInfo across the wire, so the test pins the JSON
// contract the UI reads rather than the struct it happens to be built from.
type encoderChoice struct {
	Name       db.VideoEncoder  `json:"name"`
	Codec      string           `json:"codec"`
	Vendor     ffmpeg.GPUVendor `json:"vendor"`
	Hardware   bool             `json:"hardware"`
	Available  bool             `json:"available"`
	Works      bool             `json:"works"`
	Measured   bool             `json:"measured"`
	Reason     string           `json:"reason"`
	DurationMS int64            `json:"durationMs"`
	Default    bool             `json:"default"`
}

type encoderListResponse struct {
	Encoders []encoderChoice `json:"encoders"`
	Default  string          `json:"default"`
	Probed   bool            `json:"probed"`
	Tested   bool            `json:"tested"`
	Hardware []string        `json:"hardware"`
	GPU      ffmpeg.GPUInfo  `json:"gpu"`
}

func (r encoderListResponse) find(t *testing.T, name db.VideoEncoder) encoderChoice {
	t.Helper()
	for _, e := range r.Encoders {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("%s missing from the encoder list", name)
	return encoderChoice{}
}

// worked and failed build the probe results a real detection would have cached.
func worked(name string, ms int64) ffmpeg.EncoderCapability {
	return ffmpeg.EncoderCapability{
		Name: name, Vendor: ffmpeg.EncoderVendorOf(name), Works: true, DurationMS: ms,
	}
}

func failed(name, reason string) ffmpeg.EncoderCapability {
	return ffmpeg.EncoderCapability{
		Name: name, Vendor: ffmpeg.EncoderVendorOf(name), Reason: reason,
	}
}

// probedTools is a build that lists every named encoder and has test-encoded
// the ones in caps. This is the shape detection produces on a real machine: a
// long list from the binary, a short list of verdicts from the hardware.
func probedTools(listed []string, caps ...ffmpeg.EncoderCapability) *ffmpeg.Tools {
	t := fakeTools(listed...)
	t.EncoderCaps = caps
	for _, c := range caps {
		if c.Works && c.Vendor != ffmpeg.VendorSoftware {
			t.HWEncoders = append(t.HWEncoders, c.Name)
		}
	}
	return t
}

func getEncoders(t *testing.T, tools *ffmpeg.Tools, query string) encoderListResponse {
	t.Helper()
	h, _, sign := renditionServer(t, tools)
	var resp encoderListResponse
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/encoders"+query, nil, http.StatusOK), &resp)
	return resp
}

// The bug this pins: a stock Linux FFmpeg lists h264_nvenc, h264_qsv,
// h264_vaapi and h264_amf on a box with no GPU at all, and the old endpoint
// reported that list as capability.
func TestEncoderListReportsTheTestEncodeNotTheBuildList(t *testing.T) {
	stockLinuxBuild := []string{
		string(db.EncoderX264), string(db.EncoderX265),
		string(db.EncoderNVENCH264), string(db.EncoderNVENCHEVC),
		string(db.EncoderQSVH264), string(db.EncoderVAAPIH264), string(db.EncoderAMFH264),
	}

	t.Run("a listed encoder that failed its test encode is not offered", func(t *testing.T) {
		resp := getEncoders(t, probedTools(stockLinuxBuild,
			worked(string(db.EncoderX264), 40),
			failed(string(db.EncoderNVENCH264), "Cannot load libcuda.so.1"),
		), "")

		nvenc := resp.find(t, db.EncoderNVENCH264)
		if !nvenc.Available {
			t.Error("available = false on an encoder the build registers")
		}
		if nvenc.Works {
			t.Error("works = true on an encoder whose test encode failed")
		}
		if !nvenc.Measured {
			t.Error("measured = false on an encoder that was actually probed")
		}
		if nvenc.Reason != "Cannot load libcuda.so.1" {
			t.Errorf("reason = %q, want FFmpeg's own words", nvenc.Reason)
		}
	})

	t.Run("a failing encoder stays in the list rather than vanishing", func(t *testing.T) {
		resp := getEncoders(t, probedTools(stockLinuxBuild,
			worked(string(db.EncoderX264), 40),
			failed(string(db.EncoderVAAPIH264), "Failed to initialise VAAPI connection"),
		), "")
		if len(resp.Encoders) != len(db.KnownEncoders) {
			t.Fatalf("got %d encoders, want all %d — a shorter list teaches the user nothing",
				len(resp.Encoders), len(db.KnownEncoders))
		}
	})

	t.Run("the vendor is named so a reason reads against the machine", func(t *testing.T) {
		resp := getEncoders(t, probedTools(stockLinuxBuild, worked(string(db.EncoderX264), 40)), "")
		for _, tc := range []struct {
			name db.VideoEncoder
			want ffmpeg.GPUVendor
		}{
			{db.EncoderNVENCH264, ffmpeg.VendorNVIDIA},
			{db.EncoderQSVH264, ffmpeg.VendorIntel},
			{db.EncoderVAAPIHEVC, ffmpeg.VendorIntel},
			{db.EncoderAMFH264, ffmpeg.VendorAMD},
			{db.EncoderVideoToolboxH264, ffmpeg.VendorApple},
			{db.EncoderX264, ffmpeg.VendorSoftware},
		} {
			if got := resp.find(t, tc.name).Vendor; got != tc.want {
				t.Errorf("%s vendor = %q, want %q", tc.name, got, tc.want)
			}
		}
	})

	t.Run("the working hardware encoder is marked as the default", func(t *testing.T) {
		resp := getEncoders(t, probedTools(stockLinuxBuild,
			worked(string(db.EncoderNVENCH264), 300),
			failed(string(db.EncoderQSVH264), "Error initializing an internal MFX session"),
			failed(string(db.EncoderVAAPIH264), "Failed to initialise VAAPI connection"),
			failed(string(db.EncoderAMFH264), "DLL amfrt64.dll failed to open"),
			worked(string(db.EncoderX264), 40),
		), "")
		if resp.Default != string(db.EncoderNVENCH264) {
			t.Errorf("default = %q, want the working hardware encoder", resp.Default)
		}
		if !resp.find(t, db.EncoderNVENCH264).Default {
			t.Error("the default encoder is not flagged in the list")
		}
		// The HEVC sibling rides along on the same driver, so both are offerable
		// and none of the three families that failed is.
		want := []string{string(db.EncoderNVENCH264), string(db.EncoderNVENCHEVC)}
		if !slices.Equal(resp.Hardware, want) {
			t.Errorf("hardware = %v, want %v", resp.Hardware, want)
		}
	})

	t.Run("no hardware passes, so the list offers none", func(t *testing.T) {
		resp := getEncoders(t, probedTools(stockLinuxBuild,
			failed(string(db.EncoderNVENCH264), "No CUDA capable devices found"),
			failed(string(db.EncoderQSVH264), "Error initializing an internal MFX session"),
			failed(string(db.EncoderVAAPIH264), "Failed to initialise VAAPI connection"),
			failed(string(db.EncoderAMFH264), "DLL amfrt64.dll failed to open"),
			worked(string(db.EncoderX264), 40),
		), "")
		if len(resp.Hardware) != 0 {
			t.Errorf("hardware = %v, want empty on a machine where none passed", resp.Hardware)
		}
		if resp.Default != string(db.EncoderX264) {
			t.Errorf("default = %q, want libx264", resp.Default)
		}
		if !resp.Tested {
			t.Error("tested = false after a probe that produced verdicts")
		}
	})
}

// An HEVC encoder is never probed itself — only the H.264 encoder of each
// family is — so its verdict is inherited, and must be labelled as inherited.
func TestEncoderListInfersTheHEVCSiblingFromTheProbedOne(t *testing.T) {
	listed := []string{
		string(db.EncoderX264), string(db.EncoderX265),
		string(db.EncoderNVENCH264), string(db.EncoderNVENCHEVC),
		string(db.EncoderQSVH264), string(db.EncoderQSVHEVC),
	}

	resp := getEncoders(t, probedTools(listed,
		failed(string(db.EncoderNVENCH264), "Cannot load libcuda.so.1"),
		worked(string(db.EncoderQSVH264), 120),
		worked(string(db.EncoderX264), 40),
	), "")

	hevcNvenc := resp.find(t, db.EncoderNVENCHEVC)
	if hevcNvenc.Works {
		t.Error("hevc_nvenc offered as working while h264_nvenc cannot load the driver")
	}
	if hevcNvenc.Measured {
		t.Error("measured = true on an inherited verdict; the UI must be able to say which it is")
	}
	if !strings.Contains(hevcNvenc.Reason, string(db.EncoderNVENCH264)) {
		t.Errorf("reason = %q, want it to name the encoder the verdict came from", hevcNvenc.Reason)
	}

	if hevcQSV := resp.find(t, db.EncoderQSVHEVC); !hevcQSV.Works {
		t.Error("hevc_qsv withheld while its H.264 sibling passed")
	}

	// libx265 is a different library, not a different codec on the same device,
	// so libx264's result says nothing about it.
	if x265 := resp.find(t, db.EncoderX265); !x265.Works || x265.Reason != "" {
		t.Errorf("libx265 = %+v, want no verdict inherited from libx264", x265)
	}
}

// Detection that could not run must never be the thing that takes choices away.
func TestEncoderListAssumesTheBestWhenNothingWasProbed(t *testing.T) {
	t.Run("no test encode ran", func(t *testing.T) {
		resp := getEncoders(t, fakeTools(string(db.EncoderX264), string(db.EncoderNVENCH264)), "")
		if resp.Tested {
			t.Error("tested = true with no probe results")
		}
		nvenc := resp.find(t, db.EncoderNVENCH264)
		if !nvenc.Works || nvenc.Measured {
			t.Errorf("nvenc = %+v, want works with measured=false when nothing probed it", nvenc)
		}
	})

	// The probe records what it could not run as not working, with a reason
	// that says so. Read literally that empties the editor because one scan was
	// interrupted, which is the same shape as the SRT check that used to refuse
	// to start the server.
	t.Run("a probe that never ran withholds nothing", func(t *testing.T) {
		resp := getEncoders(t, probedTools(
			[]string{string(db.EncoderX264), string(db.EncoderNVENCH264), string(db.EncoderNVENCHEVC)},
			failed(string(db.EncoderNVENCH264), "not probed: detection was cancelled"),
			failed(string(db.EncoderX264), "not probed: overall detection budget expired"),
		), "")
		for _, name := range []db.VideoEncoder{
			db.EncoderX264, db.EncoderNVENCH264, db.EncoderNVENCHEVC,
		} {
			e := resp.find(t, name)
			if !e.Works {
				t.Errorf("%s withheld on a probe that was never run: %q", name, e.Reason)
			}
			if e.Measured {
				t.Errorf("%s reported as measured when nothing was measured", name)
			}
		}
		if resp.Tested {
			t.Error("tested = true when every cached verdict was 'not probed'")
		}
	})

	t.Run("the build list was unreadable too", func(t *testing.T) {
		resp := getEncoders(t, fakeTools(), "")
		if resp.Probed {
			t.Error("probed = true with an empty encoder list")
		}
		for _, e := range resp.Encoders {
			if !e.Available || !e.Works {
				t.Errorf("%s = %+v, want every encoder offered when detection told us nothing", e.Name, e)
			}
		}
	})
}

// An encoder this build does not contain cannot work whatever the hardware is,
// and the list has to say so in one field so the UI need not combine two.
func TestEncoderListMarksAnEncoderTheBuildLacksAsNotWorking(t *testing.T) {
	resp := getEncoders(t, probedTools(
		[]string{string(db.EncoderX264)},
		worked(string(db.EncoderX264), 40),
	), "")

	nvenc := resp.find(t, db.EncoderNVENCH264)
	if nvenc.Available {
		t.Error("available = true on an encoder the build does not register")
	}
	if nvenc.Works {
		t.Error("works = true on an encoder the build does not register")
	}
	if nvenc.Reason == "" {
		t.Error("no reason given for an encoder the build lacks")
	}
}

// The re-detect button's request. It must re-measure rather than answer from
// the startup snapshot, and it must not fail when the measurement does.
func TestEncoderRedetectReplacesTheStartupSnapshot(t *testing.T) {
	// A cached claim that NVENC works, on a Tools whose FFmpeg path cannot
	// exec — so a genuine re-probe can only conclude that it does not.
	tools := probedTools(
		[]string{string(db.EncoderX264), string(db.EncoderNVENCH264)},
		worked(string(db.EncoderNVENCH264), 300),
		worked(string(db.EncoderX264), 40),
	)

	resp := getEncoders(t, tools, "?redetect=1")
	if resp.find(t, db.EncoderNVENCH264).Works {
		t.Error("nvenc still reported working after a re-detect that could not run it")
	}
	if len(resp.Hardware) != 0 {
		t.Errorf("hardware = %v, want empty after a re-detect found none", resp.Hardware)
	}
	if resp.Default != string(db.EncoderX264) {
		t.Errorf("default = %q, want libx264 once no hardware survives", resp.Default)
	}
	if resp.GPU.Platform == "" {
		t.Error("no GPU enumeration returned; the editor has nothing to explain a failure with")
	}
}
