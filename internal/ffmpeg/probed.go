package ffmpeg

import (
	"context"
	"encoding/json"
)

// Probed is one generation of what this machine proved: the GPU enumeration
// and the encoder verdicts that were probed against it, read together.
//
// RefreshEncoderCapabilities swaps both under one lock, but a consumer that
// asks for them in two calls can straddle a re-detect and pair a verdict with
// a render node it was not proved on. Everything that decides from both (the
// rendition start, /system, /encoders) reads one Probed instead.
type Probed struct {
	// GPU is the enumeration of this generation. Zero when it was not asked for.
	GPU GPUInfo

	view      *Tools
	toolsJSON json.RawMessage
}

// Probed returns one consistent generation. wantGPU says whether the
// enumeration is needed, so a consumer with no VAAPI encoder in play does not
// pay for a scan. A nil Tools has proved nothing: every encoder reads as
// working, as with an unprobed install.
func (t *Tools) Probed(ctx context.Context, wantGPU bool) Probed {
	if t == nil {
		p := Probed{view: &Tools{}}
		if wantGPU {
			p.GPU = DetectGPUs(ctx)
		}
		return p
	}
	for attempt := 0; ; attempt++ {
		t.mu.RLock()
		if !wantGPU || t.gpuDone || attempt >= 2 {
			p := t.probedLocked(wantGPU)
			t.mu.RUnlock()
			return p
		}
		t.mu.RUnlock()
		t.GPUs(ctx)
	}
}

func (t *Tools) probedLocked(wantGPU bool) Probed {
	p := Probed{view: &Tools{
		VideoEncoders: append([]string(nil), t.VideoEncoders...),
		HWEncoders:    append([]string(nil), t.HWEncoders...),
		EncoderCaps:   append([]EncoderCapability(nil), t.EncoderCaps...),
	}}
	if wantGPU {
		p.GPU = t.gpu
	}
	p.toolsJSON, _ = t.marshalLocked()
	return p
}

// ToolsJSON is the Tools document of this generation, as GET /system serves it.
func (p Probed) ToolsJSON() json.RawMessage { return p.toolsJSON }

// Capability, EncoderWorks, HasEncoder, DefaultVideoEncoder and Listed answer
// from this generation, with the semantics of the Tools methods of that name.
func (p Probed) Capability(name string) (EncoderCapability, bool) { return p.view.Capability(name) }
func (p Probed) EncoderWorks(name string) (bool, string)          { return p.view.EncoderWorks(name) }
func (p Probed) HasEncoder(name string) bool                      { return p.view.HasEncoder(name) }
func (p Probed) DefaultVideoEncoder() string                      { return p.view.DefaultVideoEncoder() }

// Listed reports whether `ffmpeg -encoders` ran: an empty list means "we do not
// know", not "encodes nothing".
func (p Probed) Listed() bool { return len(p.view.VideoEncoders) > 0 }
