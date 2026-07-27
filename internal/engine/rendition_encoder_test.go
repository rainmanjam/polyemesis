package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A rendition is refused for two different reasons, and the difference matters
// to whoever reads the error: "this build has no h264_nvenc" is fixed by
// installing a different FFmpeg, "h264_nvenc did not pass its test encode" is
// fixed by installing a driver. Neither may fire when detection could not run.
func TestRenditionEncoderProblem(t *testing.T) {
	listed := []string{
		string(db.EncoderX264), string(db.EncoderNVENCH264), string(db.EncoderVAAPIH264),
	}

	tools := func(video []string, caps ...ffmpeg.EncoderCapability) *ffmpeg.Tools {
		return &ffmpeg.Tools{
			FFmpeg: "/nonexistent/ffmpeg", Version: "7.1", Major: 7, Minor: 1,
			VideoEncoders: video, EncoderCaps: caps,
		}
	}
	ok := func(name string) ffmpeg.EncoderCapability {
		return ffmpeg.EncoderCapability{Name: name, Vendor: ffmpeg.EncoderVendorOf(name), Works: true}
	}
	bad := func(name, reason string) ffmpeg.EncoderCapability {
		return ffmpeg.EncoderCapability{Name: name, Vendor: ffmpeg.EncoderVendorOf(name), Reason: reason}
	}

	tests := []struct {
		name    string
		tools   *ffmpeg.Tools
		encoder db.VideoEncoder
		// wantErr is a substring the message must carry; "" means it must start.
		wantErr string
	}{
		{
			name:    "an encoder that passed its test encode starts",
			tools:   tools(listed, ok(string(db.EncoderX264)), ok(string(db.EncoderNVENCH264))),
			encoder: db.EncoderNVENCH264,
		},
		{
			name:    "an encoder the build does not register is refused as a build problem",
			tools:   tools([]string{string(db.EncoderX264)}, ok(string(db.EncoderX264))),
			encoder: db.EncoderNVENCH264,
			wantErr: "this FFmpeg build has no h264_nvenc encoder",
		},
		{
			name: "a listed encoder that failed its test encode is refused with FFmpeg's reason",
			tools: tools(listed,
				ok(string(db.EncoderX264)),
				bad(string(db.EncoderNVENCH264), "Cannot load libcuda.so.1"),
			),
			encoder: db.EncoderNVENCH264,
			wantErr: "Cannot load libcuda.so.1",
		},
		{
			name: "the GPU was swapped out from under a saved rendition",
			tools: tools(listed,
				ok(string(db.EncoderX264)),
				bad(string(db.EncoderVAAPIH264), "Permission denied"),
			),
			encoder: db.EncoderVAAPIH264,
			wantErr: "Permission denied",
		},
		{
			name:    "a failure with no message still names the encoder",
			tools:   tools(listed, bad(string(db.EncoderNVENCH264), "")),
			encoder: db.EncoderNVENCH264,
			wantErr: "the test encode failed without saying why",
		},
		{
			name:    "an encoder nothing probed is allowed to start",
			tools:   tools(listed, ok(string(db.EncoderX264))),
			encoder: db.EncoderNVENCH264,
		},
		{
			name:    "detection that produced nothing at all stops nothing",
			tools:   tools(nil),
			encoder: db.EncoderNVENCH264,
		},
		{
			// Only the H.264 encoder of each family is probed. The editor infers
			// the HEVC sibling's verdict from it and stops offering the choice;
			// the engine deliberately does not, because refusing to start a
			// stream on an inference is worse than not offering it on one.
			name: "an HEVC encoder is not refused on its H.264 sibling's failure",
			tools: tools(append(listed, string(db.EncoderNVENCHEVC)),
				bad(string(db.EncoderNVENCH264), "No CUDA capable devices found")),
			encoder: db.EncoderNVENCHEVC,
		},
		{
			name:    "no detected FFmpeg at all stops nothing",
			tools:   nil,
			encoder: db.EncoderNVENCH264,
		},
		{
			// One interrupted detection must not take the whole install down
			// with it. The probe marks what it could not run as not working;
			// that is an absence of evidence, not evidence.
			name:    "a probe that was cancelled is not treated as a failure",
			tools:   tools(listed, bad(string(db.EncoderNVENCH264), "not probed: detection was cancelled")),
			encoder: db.EncoderNVENCH264,
		},
		{
			name:    "a probe that ran out of budget is not treated as a failure",
			tools:   tools(listed, bad(string(db.EncoderVAAPIH264), "not probed: overall detection budget expired")),
			encoder: db.EncoderVAAPIH264,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := renditionEncoderProblem(tt.tools, tt.encoder)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("renditionEncoderProblem() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("renditionEncoderProblem() = nil, want an error mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("renditionEncoderProblem() = %q, want it to carry %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), string(tt.encoder)) {
				t.Errorf("renditionEncoderProblem() = %q, want it to name the encoder", err)
			}
		})
	}
}
