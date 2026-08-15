package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// ------------------------------------------------------------- renditions API
//
// A rendition is one shared video encode that any number of destinations can
// select, so N destinations wanting 1080p60 cost one encode rather than N. It
// re-encodes video only: every audio track passes through untouched, and each
// destination keeps doing -c:v copy plus its own routing graph on top. A
// destination with no rendition is passthrough, which is the default and what
// every pre-renditions install already does.

// renditionUsage is how many destinations point at each rendition, split by
// whether they are enabled.
//
// Enabled is the ref count that decides whether an encode runs at all; the
// total also counts disabled rows, because deleting a rendition drops every one
// of them back to passthrough. Both come from a single pass over the
// destinations so the two numbers can never disagree with each other, which
// they could if one were read from CountEnabledDestinationsByRendition a query
// later.
func (s *Server) renditionUsage() (total, enabled map[int64]int, err error) {
	rows, err := s.store.ListDestinations()
	if err != nil {
		return nil, nil, err
	}
	total, enabled = map[int64]int{}, map[int64]int{}
	for _, row := range rows {
		if row.RenditionID == nil {
			continue
		}
		total[*row.RenditionID]++
		if row.Enabled {
			enabled[*row.RenditionID]++
		}
	}
	return total, enabled, nil
}

// renditionView is one row plus the counts the UI needs to say "used by 3
// destinations" and to warn before a delete, without a second round trip.
func renditionView(r *db.Rendition, total, enabled map[int64]int) map[string]any {
	return map[string]any{
		"rendition":           r,
		"destinations":        total[r.ID],
		"enabledDestinations": enabled[r.ID],
	}
}

func (s *Server) handleListRenditions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListRenditions()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, renditionView(row, total, enabled))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	row, err := s.store.GetRendition(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, renditionView(row, total, enabled))
}

func (s *Server) handleCreateRendition(w http.ResponseWriter, r *http.Request) {
	var row db.Rendition
	if !decodeJSON(w, r, &row) {
		return
	}
	row.ID = 0
	// The store fills in encoder, preset and GOP before validating, so the
	// smallest useful payload is {name, height, videoBitrate}.
	created, err := s.store.CreateRendition(&row)
	if err != nil {
		// The same lift CreateDestination takes: a payload that names no
		// source is ordinary, and an install with none is not a bad request.
		writeCreateError(w, err)
		return
	}
	// Nothing selects a brand-new rendition yet, so this starts no encode; it
	// runs for the same reason every other mutation reconciles, which is that
	// the saved state and the running state are never allowed to drift.
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after rendition create", "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rendition": created})
}

func (s *Server) handleUpdateRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetRendition(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Decode over the existing row so a client sending only a bitrate does not
	// blank the name, exactly as the destination editor does.
	if !decodeJSON(w, r, existing) {
		return
	}
	existing.ID = id

	updated, err := s.store.UpdateRendition(existing)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The rendition's signature rides in each downstream destination's, so this
	// restarts the encode and exactly the destinations reading it, and nothing
	// else.
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after rendition update", "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rendition": updated})
}

// handleDeleteRendition removes a rendition and reports what that cost.
//
// The delete succeeds even while destinations are using it: the foreign key
// nulls their rendition_id, so they survive, stay enabled, and fall back to
// passthrough. That is the safe database outcome and the wrong thing to do
// silently — a destination that was being fed 1080p60 because its platform will
// not take the 4K source is now being handed the 4K source, and the first the
// user hears of it may be the platform rejecting the stream. So the counts are
// taken before the delete and returned with an explicit warning.
func (s *Server) handleDeleteRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	total, enabled, err := s.renditionUsage()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.store.DeleteRendition(id); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after rendition delete", "err", err)
	}

	resp := map[string]any{
		"status":              "deleted",
		"destinations":        total[id],
		"enabledDestinations": enabled[id],
	}
	if n := total[id]; n > 0 {
		resp["warning"] = renditionDeleteWarning(n, enabled[id])
	}
	writeJSON(w, http.StatusOK, resp)
}

func renditionDeleteWarning(total, enabled int) string {
	subject := "destinations have"
	if total == 1 {
		subject = "destination has"
	}
	msg := fmt.Sprintf("%d %s fallen back to passthrough and will be sent the source video "+
		"unchanged. Check the source still fits what each platform accepts.", total, subject)
	if enabled > 0 {
		live := "are"
		if enabled == 1 {
			live = "is"
		}
		msg += fmt.Sprintf(" %d of them %s enabled and restarting now.", enabled, live)
	}
	return msg
}

// ---------------------------------------------------------------- presets

// handleRenditionPresets returns everything the create-a-rendition form needs:
// the starting points, the disclaimer that must be shown beside them, and the
// bounds the number inputs should use.
//
// The presets carry conservative numbers and each one's note already ends with
// the disclaimer. Platform ceilings move and differ by partner status, so the
// note is rendered verbatim and no ceiling here is presented as authoritative.
func (s *Server) handleRenditionPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"presets":    db.RenditionPresets(),
		"disclaimer": db.PresetDisclaimer,
		"bounds": map[string]any{
			"minDimension":  db.MinRenditionDimension,
			"maxDimension":  db.MaxRenditionDimension,
			"maxFps":        db.MaxRenditionFPS,
			"minBitrate":    db.MinRenditionBitrate,
			"maxBitrate":    db.MaxRenditionBitrate,
			"minGopSeconds": db.MinRenditionGOP,
			"maxGopSeconds": db.MaxRenditionGOP,
		},
	})
}

// ------------------------------------------------------------------- fonts

// handleListFonts is what the text overlay's font picker is built from.
//
// It reports the fonts actually ON DISK rather than a list compiled into the
// UI, because the whole point of <dataDir>/fonts is that an operator can drop
// their own in. A hardcoded list would show the two built-ins forever and the
// feature would look broken to the first person who added a third.
//
// It also reports whether this FFmpeg can draw text at all. drawtext needs
// libfreetype compiled in, and a build without it has no such filter -- a
// Homebrew FFmpeg on macOS is exactly that. Saying so here lets the editor
// explain why the controls are disabled instead of accepting settings that
// silently never render.
func (s *Server) handleListFonts(w http.ResponseWriter, r *http.Request) {
	names, err := ffmpeg.ListFonts(filepath.Join(s.cfg.DataDir, ffmpeg.FontsDirName))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	builtin := map[string]bool{}
	for _, b := range ffmpeg.BuiltinFonts {
		builtin[b] = true
	}
	type fontInfo struct {
		Name string `json:"name"`
		// BuiltIn marks the ones polyemesis rewrites on every startup. The UI
		// warns rather than forbids: an operator who replaces one will find it
		// restored after a restart, and that is worth saying before they try.
		BuiltIn bool `json:"builtIn"`
	}
	out := make([]fontInfo, 0, len(names))
	for _, n := range names {
		out = append(out, fontInfo{Name: n, BuiltIn: builtin[n]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fonts":       out,
		"defaultFont": ffmpeg.DefaultFont,
		// The directory, spelled out, so the UI can tell the operator WHERE to
		// put a font rather than leaving them to guess.
		"dir":           filepath.Join(s.cfg.DataDir, ffmpeg.FontsDirName),
		"textSupported": s.tools().HasFilter("drawtext"),
		"bounds": map[string]any{
			"maxTextLen":      db.MaxTextLen,
			"minSizePct":      db.MinTextSizePct,
			"maxSizePct":      db.MaxTextSizePct,
			"maxMarginPct":    db.MaxTextMarginPct,
			"anchors":         ffmpeg.OverlayAnchors,
			"defaultColor":    ffmpeg.DefaultTextColor,
			"defaultBoxColor": ffmpeg.DefaultTextBoxColor,
		},
	})
}

// ---------------------------------------------------------------- encoders

// encoderInfo is one choice in the rendition editor's encoder list.
//
// Available and Works are deliberately two fields. Available answers "does this
// binary contain the encoder", Works answers "did it encode a frame on this
// machine just now", and the gap between them is the entire problem: a stock
// Linux FFmpeg is Available for nvenc, qsv, vaapi and amf on a box with no GPU
// in it, and the user only finds out which after they have gone live.
type encoderInfo struct {
	Name  db.VideoEncoder `json:"name"`
	Codec string          `json:"codec"`
	// Vendor is the silicon behind the encoder, so the list can say "NVIDIA"
	// rather than "hardware" and a failure reads against what is in the machine.
	Vendor ffmpeg.GPUVendor `json:"vendor"`
	// Hardware marks the vendor-accelerated encoders, which are the ones whose
	// behaviour depends on the driver rather than on us.
	Hardware bool `json:"hardware"`
	// Available is whether this FFmpeg registers the encoder.
	Available bool `json:"available"`
	// Works is whether the encoder is usable here. Unknown counts as usable:
	// detection that could not run must not take choices away.
	Works bool `json:"works"`
	// Measured distinguishes a verdict from a test encode of this exact encoder
	// from one that was assumed or inferred, so the UI can say which it is.
	Measured bool `json:"measured"`
	// Reason is FFmpeg's own words when Works is false. "No CUDA capable
	// devices found", "Cannot load libcuda.so.1" and "Permission denied" are
	// three different problems with three different fixes, and only the message
	// tells them apart.
	Reason string `json:"reason,omitempty"`
	// DurationMS is how long the test encode took. A hardware encoder that needs
	// two seconds to open one frame is usually a driver falling back to software.
	DurationMS int64 `json:"durationMs,omitempty"`
	// Default marks the one a new rendition starts on.
	Default bool `json:"default"`
}

// handleListEncoders reports what this machine can encode with and, for
// everything it cannot, why not.
//
// Every known encoder is listed rather than only the working ones. A shorter
// list teaches nobody anything: "h264_nvenc — no NVENC capable device found"
// tells the user their container is missing --gpus, and a rendition saved on a
// machine that had QSV must still render its own encoder in the form after the
// install moves to one that does not.
//
// `?redetect=1` re-runs the hardware scan and every test encode before
// answering, which is what the editor's re-detect button sends. It is a GET
// because it is a read of the machine's current state — the same answer, just
// not from the cache — and because a driver install or a --device passthrough
// that happened after launch is invisible until something asks again.
func (s *Server) handleListEncoders(w http.ResponseWriter, r *http.Request) {
	tools := s.tools()

	gpu := machineGPUs(r.Context())
	if r.URL.Query().Get("redetect") != "" {
		// The plain listing is a read and stays reachable by a read-scoped
		// token. This is not: it spawns a test encode per candidate encoder,
		// enumerates GPU device nodes, and overwrites install-wide capability
		// state under a global mutex for up to redetectCeiling -- on a context
		// deliberately detached from the request, so the caller cannot even
		// abort what it started.
		//
		// Gated in the handler rather than by denying the route, because the
		// two behaviours share a URL and only one of them is the problem.
		// Renaming it to a POST would be the tidier shape and is a UI change
		// this security fix has no business making.
		if isReadScopedToken(r) {
			writeError(w, http.StatusForbidden,
				"re-detecting hardware runs test encodes and rewrites this install's "+
					"encoder capabilities; that needs a token with the \"admin\" scope")
			return
		}
		gpu = redetectHardware(r.Context(), tools)
	}

	// An empty list means `ffmpeg -encoders` did not run or failed, not that the
	// binary encodes nothing. Detection treats that as "assume the best" and so
	// must this: claiming every encoder is unavailable would leave the user
	// unable to create any rendition at all.
	listed := len(tools.VideoEncoders) > 0
	def := tools.DefaultVideoEncoder()

	out := make([]encoderInfo, 0, len(db.KnownEncoders))
	// Derived from the same pass that builds the list rather than read off
	// Tools.HWEncoders, which a concurrent re-detect is free to be rewriting.
	// It also cannot then disagree with the list beside it.
	working := []string{}
	// Whether anything was actually encoded here. A cached verdict that says
	// only "not probed" is not a measurement, so it does not count.
	tested := false
	for _, e := range db.KnownEncoders {
		info := encoderInfo{
			Name:      e,
			Codec:     e.Codec(),
			Vendor:    ffmpeg.EncoderVendorOf(string(e)),
			Hardware:  isHardwareEncoder(e),
			Available: !listed || tools.HasEncoder(string(e)),
			Default:   string(e) == def,
		}
		info.Works, info.Measured, info.Reason, info.DurationMS = encoderVerdict(tools, e)
		// An encoder the build does not contain cannot work regardless of what
		// the machine has, and saying so in one field keeps the UI from having
		// to reason about the combination.
		if !info.Available {
			info.Works = false
			if info.Reason == "" {
				info.Reason = "this FFmpeg build does not include " + string(e)
			}
		}
		if info.Hardware && info.Works {
			working = append(working, string(e))
		}
		tested = tested || info.Measured
		out = append(out, info)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"encoders": out,
		"default":  def,
		// probed keeps its original meaning — the build's encoder list was
		// readable — because that is still the flag that says whether Available
		// means anything.
		"probed": listed,
		// tested is the new one: whether anything was actually encoded. False
		// means every Works above is an assumption.
		"tested": tested,
		// The hardware encoders that are usable here. Empty is the answer that
		// matters: it is the machine saying there is nothing to offload onto, and
		// the editor stops telling the user to "choose a hardware encoder".
		"hardware": working,
		"gpu":      gpu,
	})
}

// encoderVerdict answers whether this encoder is usable here, how sure we are,
// and why not when it is not.
//
// Three cases in descending order of evidence: this encoder was test-encoded;
// its H.264 sibling was and failed; nothing was probed at all. The last is
// reported as working, because an encoder nobody asked about is not an encoder
// that was found wanting.
func encoderVerdict(tools *ffmpeg.Tools, e db.VideoEncoder) (works, measured bool, reason string, ms int64) {
	if c, ok := tools.Capability(string(e)); ok && !notProbed(c.Reason) {
		return c.Works, true, c.Reason, c.DurationMS
	}
	if sib := h264SiblingOf(e); sib != "" {
		if c, ok := tools.Capability(sib); ok && !c.Works && !notProbed(c.Reason) {
			return false, false, fmt.Sprintf("%s opens the same device through the same driver and failed: %s", sib, c.Reason), 0
		}
	}
	return true, false, "", 0
}

// notProbed distinguishes "the encoder failed" from "we never got to ask it".
//
// Detection marks a probe it could not run — a cancelled scan, an expired
// budget — as not working, with a reason that says so. Read literally, that
// would take every encoder out of the editor because one scan was interrupted.
// Nothing was demonstrated in that case, so nothing may be withheld on it.
func notProbed(reason string) bool {
	return strings.HasPrefix(reason, "not probed:")
}

// h264SiblingOf names the encoder whose test encode stands in for one that was
// never tested itself.
//
// Only the H.264 encoder of each hardware family is probed, and the HEVC
// encoder beside it opens the same device through the same driver: if
// h264_nvenc cannot load libcuda then neither can hevc_nvenc, and there is no
// machine where one of those is true and the other is not. Software is left out
// on purpose — libx264 encoding here says nothing about whether this build has
// x265, which the encoder list answers on its own.
//
// The inference is good enough to stop offering a choice, and deliberately not
// good enough to refuse a start: the engine consults measured results only, so
// a rendition already saved on hevc_qsv is never killed on a guess.
func h264SiblingOf(e db.VideoEncoder) string {
	if rest, ok := strings.CutPrefix(string(e), "hevc_"); ok {
		return "h264_" + rest
	}
	return ""
}

// isHardwareEncoder splits the list the way the UI groups it. libx264 and
// libx265 are the software encoders; everything else in KnownEncoders is a
// wrapper around a vendor's fixed-function block.
func isHardwareEncoder(e db.VideoEncoder) bool {
	return e != db.EncoderX264 && e != db.EncoderX265
}

// ------------------------------------------------------------ hardware scan

// hardware caches the GPU enumeration.
//
// It is package scope rather than a Server field because it describes the
// machine, not a server instance — there is one of each per process, and a
// second Server in a test is still looking at the same PCI bus. The mutex is
// also the single-flight: a user leaning on the re-detect button must not spawn
// one set of test encodes per click.
var hardware struct {
	mu   sync.Mutex
	info ffmpeg.GPUInfo
	done bool
}

// machineGPUs returns the cached enumeration, scanning once on first ask.
//
// The scan is capped at three seconds and cannot fail, so a wedged driver costs
// a slow first request rather than a broken page.
func machineGPUs(ctx context.Context) ffmpeg.GPUInfo {
	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	if !hardware.done {
		hardware.info = ffmpeg.DetectGPUs(ctx)
		hardware.done = true
	}
	return hardware.info
}

// redetectHardware re-enumerates the GPUs and re-runs every test encode.
//
// This is the answer to hardware that moved after launch: a driver package
// upgraded, a card passed into the container after the fact, a laptop back from
// suspend with a render node that now opens. Neither half can fail, so there is
// nothing to return but the new answer.
//
// The request's cancellation is deliberately dropped. A probe that is cancelled
// reports every encoder as not working, and that verdict is cached — so a user
// who closes the tab mid-scan would leave the install believing it has no
// encoders at all, and every rendition would then be refused. The scan is
// self-limiting (three seconds for the devices, ten for the probes together),
// so there is nothing here that needs the client to stay to bound it.
func redetectHardware(ctx context.Context, tools *ffmpeg.Tools) ffmpeg.GPUInfo {
	scan, cancel := context.WithTimeout(context.WithoutCancel(ctx), redetectCeiling)
	defer cancel()

	hardware.mu.Lock()
	defer hardware.mu.Unlock()
	hardware.info = ffmpeg.DetectGPUs(scan)
	hardware.done = true
	tools.RefreshEncoderCapabilities(scan)
	return hardware.info
}

// redetectCeiling is a backstop, not the real bound — the GPU scan and the
// probe budget bound themselves well inside it. It exists so that a future
// change to either cannot leave this handler waiting forever.
const redetectCeiling = 30 * time.Second

// ---------------------------------------------------------------- restart

// handleRestartRendition cycles one shared encode, and with it the destinations
// reading from it. It mirrors the per-destination restart: the operator's
// escape hatch for an encoder that has wedged without dying.
func (s *Server) handleRestartRendition(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.eng().RestartRendition(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
}
