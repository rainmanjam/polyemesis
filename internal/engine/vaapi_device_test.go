package engine

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// A re-detect (GET /api/v1/encoders?redetect=1) refreshes Tools, and the engine
// must follow it: a rendition started afterwards names the node the probe just
// proved, not the one the engine pinned at its first VAAPI start.
func TestVAAPIDeviceFollowsAHardwareRedetect(t *testing.T) {
	node := "/dev/dri/renderD128"
	tools := &ffmpeg.Tools{
		GPUDetect: func(context.Context) ffmpeg.GPUInfo { return ffmpeg.GPUInfo{VAAPIDevice: node} },
	}
	e := &Engine{tools: tools, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := &db.Rendition{Encoder: db.EncoderVAAPIH264}

	if got := e.vaapiDevice(r); got != node {
		t.Fatalf("first vaapiDevice = %q, want %q", got, node)
	}

	node = "/dev/dri/renderD129"
	tools.RefreshEncoderCapabilities(context.Background())
	if got := e.vaapiDevice(r); got != node {
		t.Errorf("after a re-detect vaapiDevice = %q, want %q", got, node)
	}

	if got := e.vaapiDevice(&db.Rendition{Encoder: db.EncoderX264}); got != "" {
		t.Errorf("software rendition got render node %q", got)
	}
}
