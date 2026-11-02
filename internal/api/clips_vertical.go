package api

// The vertical-export half of the clip editor (Wave 4 #5b): turning the editor's
// layout and caption choices into a clipper.Vertical, and the saved templates
// those choices can be stored as. No route here renders anything -- the short
// is the ordinary /export job with a Vertical attached.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// clipVerticalBody is the editor's vertical layout. It is also exactly what a
// template stores, so a template applied in the editor is the body it sends.
type clipVerticalBody struct {
	Layout string `json:"layout"`
	// Focus pans the centre crop, 0..1. Absent means the middle.
	Focus      *float64      `json:"focus,omitempty"`
	Face       clipper.Rect  `json:"face,omitzero"`
	Game       clipper.Rect  `json:"game,omitzero"`
	FaceShare  float64       `json:"faceShare,omitempty"`
	FaceBottom bool          `json:"faceBottom,omitempty"`
	Captions   *clipCaptions `json:"captions,omitempty"`
}

// clipCaptions is whether to burn captions, and how they look. The words come
// from the recording's transcript, never from the request.
type clipCaptions struct {
	Enabled bool                 `json:"enabled"`
	Style   clipper.CaptionStyle `json:"style"`
}

// layout is the body as a clipper.Vertical with no cues: what a template is.
func (b clipVerticalBody) layout() clipper.Vertical {
	v := clipper.Vertical{
		Layout:     clipper.Layout(strings.TrimSpace(b.Layout)),
		Focus:      0.5,
		Face:       b.Face,
		Game:       b.Game,
		FaceShare:  b.FaceShare,
		FaceBottom: b.FaceBottom,
	}
	if b.Focus != nil {
		v.Focus = *b.Focus
	}
	if b.Captions != nil && b.Captions.Enabled {
		v.Captions = &clipper.Captions{Style: b.Captions.Style}
	}
	return v
}

// clipVertical builds the Vertical an export carries: the layout from the
// body, the caption cues from the transcript over the clip's span, the font
// from the server's own fonts directory, and whether this FFmpeg can draw text.
func (s *Server) clipVertical(tl clipTimeline, body clipRequestBody) (*clipper.Vertical, error) {
	v := body.Vertical.layout()
	if err := v.ValidateLayout(); err != nil {
		return nil, err
	}
	if v.Captions == nil {
		return &v, nil
	}
	style := v.Captions.Style.Normalized()
	font, err := s.captionFont(style.Font)
	if err != nil {
		return nil, err
	}
	var tracks []int
	if strings.TrimSpace(body.AudioMode) == string(clipper.AudioTracks) {
		// Captions follow the tracks the short carries: a clip of one
		// speaker is not captioned with the other's words.
		tracks = append(tracks, body.Tracks...)
	}
	cut, err := tl.timeline()
	if err != nil {
		return nil, err
	}
	cues, err := clipper.CaptionCues(cut.Segments(), ClipTranscriptSource(s.store),
		time.Duration(body.InMS)*time.Millisecond, time.Duration(body.OutMS)*time.Millisecond, tracks)
	if err != nil {
		return nil, err
	}
	v.Captions = &clipper.Captions{
		Cues:     cues,
		Tracks:   tracks,
		Style:    style,
		FontFile: font,
		DrawText: s.tools().HasFilter("drawtext"),
	}
	return &v, nil
}

// captionFont resolves a font NAME to a file in the server's fonts directory.
//
// The name is matched against the directory's own listing and the path built
// from the LISTED name, so nothing the request typed ever becomes part of a
// path -- the request can only choose among files that are already there.
func (s *Server) captionFont(name string) (string, error) {
	dir := s.cfg.FontsDir()
	names, err := ffmpeg.ListFonts(dir)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if n == name {
			return ffmpeg.FontPath(dir, filepath.Base(n))
		}
	}
	return "", fmt.Errorf("%w: font %q is not in the fonts directory", clipper.ErrInvalidRequest, name)
}

// ------------------------------------------------------------------ templates

// maxTemplateName bounds a template's name; maxTemplates bounds the table.
const (
	maxTemplateName = 60
	maxTemplates    = 100
)

// clipTemplatesView is the template list plus what the vertical editor needs
// to offer: the layouts, the fonts, whether captions can be drawn at all, and
// the short length the 60-second guard warns past.
type clipTemplatesView struct {
	Templates       []clipTemplateView   `json:"templates"`
	Layouts         []string             `json:"layouts"`
	Fonts           []string             `json:"fonts"`
	DrawText        bool                 `json:"drawText"`
	MaxShortSeconds float64              `json:"maxShortSeconds"`
	DefaultStyle    clipper.CaptionStyle `json:"defaultStyle"`
}

type clipTemplateView struct {
	ID        int64            `json:"id"`
	Name      string           `json:"name"`
	Vertical  clipVerticalBody `json:"vertical"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

func (s *Server) handleListClipTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListClipTemplates()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	fonts, _ := ffmpeg.ListFonts(s.cfg.FontsDir())
	if fonts == nil {
		fonts = []string{}
	}
	view := clipTemplatesView{
		Templates:       make([]clipTemplateView, 0, len(list)),
		Fonts:           fonts,
		DrawText:        s.tools().HasFilter("drawtext"),
		MaxShortSeconds: clipper.ShortMaxDuration.Seconds(),
		DefaultStyle:    clipper.DefaultCaptionStyle(),
	}
	for _, l := range clipper.Layouts() {
		view.Layouts = append(view.Layouts, string(l))
	}
	for _, t := range list {
		var body clipVerticalBody
		if err := json.Unmarshal(t.Body, &body); err != nil {
			// A row this build cannot read is left out rather than failing the
			// list: the operator can still save over it by name.
			continue
		}
		view.Templates = append(view.Templates, clipTemplateView{ID: t.ID, Name: t.Name, Vertical: body, UpdatedAt: t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, view)
}

type clipTemplateBody struct {
	Name     string           `json:"name"`
	Vertical clipVerticalBody `json:"vertical"`
}

func (s *Server) handlePutClipTemplate(w http.ResponseWriter, r *http.Request) {
	var body clipTemplateBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || utf8.RuneCountInString(name) > maxTemplateName || strings.ContainsAny(name, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("a template needs a name of 1 to %d characters", maxTemplateName))
		return
	}
	if err := body.Vertical.layout().ValidateLayout(); err != nil {
		s.writeClipError(w, err)
		return
	}
	existing, err := s.store.ListClipTemplates()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	replacing := false
	for _, t := range existing {
		replacing = replacing || t.Name == name
	}
	if !replacing && len(existing) >= maxTemplates {
		writeError(w, http.StatusConflict, fmt.Sprintf("there are already %d templates; delete one first", maxTemplates))
		return
	}
	// Re-marshalled, never stored as received: the stored body is exactly the
	// fields this build understands.
	raw, err := json.Marshal(body.Vertical)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t, err := s.store.PutClipTemplate(name, raw)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clipTemplateView{ID: t.ID, Name: t.Name, Vertical: body.Vertical, UpdatedAt: t.UpdatedAt})
}

func (s *Server) handleDeleteClipTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteClipTemplate(id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ClipTranscriptSource reads a recording's transcript as caption lines. Exported
// because the clip worker, wired in cmd/polyemesis, reads the words at run time
// through the same adapter the editor plans with.
func ClipTranscriptSource(store *db.DB) clipper.TranscriptSource {
	return func(recordingID int64) ([]clipper.TranscriptLine, error) {
		tr, err := store.GetTranscript(recordingID)
		if errors.Is(err, db.ErrNotFound) || (err == nil && tr == nil) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		merged := tr.Merged()
		out := make([]clipper.TranscriptLine, 0, len(merged))
		for _, seg := range merged {
			out = append(out, clipper.TranscriptLine{Track: seg.Track, StartMS: seg.StartMS, EndMS: seg.EndMS, Text: seg.Text})
		}
		return out, nil
	}
}
