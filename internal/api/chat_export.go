package api

// Chat export: a recording's chat as JSON or WebVTT, and any window of chat as
// JSON, CSV or NDJSON.
//
// BOTH ROUTES ARE READ-DENIED, like GET /chat/messages and for the same reason:
// the body is what each viewer wrote, under their name. They are registered with
// the other downloads because the UI opens them as top-level navigations, which
// cannot carry a CSRF header, and a fetch-to-blob would hold the whole file in
// the browser and give up the streaming.
//
// Every parameter is validated before the first byte is written: a stream
// cannot change its status code once it has started.

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chatexport"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// chatExportContentTypes maps each accepted format to its media type.
var chatExportContentTypes = map[string]string{
	"json":   "application/json; charset=utf-8",
	"vtt":    "text/vtt; charset=utf-8",
	"csv":    "text/csv; charset=utf-8",
	"ndjson": "application/x-ndjson; charset=utf-8",
}

// attachment builds a Content-Disposition from a name the server chose.
func attachment(name string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

// startChatDownload sets the headers every chat download shares.
func startChatDownload(w http.ResponseWriter, format, filename string) {
	h := w.Header()
	h.Set("Content-Type", chatExportContentTypes[format])
	h.Set("Content-Disposition", attachment(filename))
	h.Set("X-Content-Type-Options", "nosniff")
	// Viewer names: nothing in a shared cache.
	h.Set("Cache-Control", "no-store")
}

// handleRecordingChat serves one recording's chat. The saved sidecar is served
// as written when there is one; otherwise the file is built from the chat table
// if that still holds the recording's window, and a 404 says so when it does not.
func (s *Server) handleRecordingChat(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, errInvalidID)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "vtt" {
		writeError(w, http.StatusBadRequest, "format must be json or vtt")
		return
	}
	rec, err := s.store.GetRecording(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	jsonName, vttName := chatexport.SidecarNames(rec.Filename)
	name := jsonName
	if format == "vtt" {
		name = vttName
	}

	if path, ok := s.recordings().ChatSidecar(rec.Filename, format == "vtt"); ok {
		f, err := os.Open(path)
		if err == nil {
			defer f.Close()
			if fi, err := f.Stat(); err == nil {
				startChatDownload(w, format, name)
				// ServeContent gives range requests and If-Modified-Since.
				http.ServeContent(w, r, name, fi.ModTime(), f)
				return
			}
		}
		// A sidecar that vanished between the stat and the open falls through to
		// the table, which is the honest second source.
	}

	plan, err := s.recordings().ChatPlan(*rec)
	if err != nil && !errors.Is(err, chatexport.ErrNotRetained) {
		writeStoreError(w, err)
		return
	}
	if err != nil || !plan.Retained() {
		writeError(w, http.StatusNotFound, "chat for this recording was not saved with it and is no longer "+
			"stored; the chat history setting decides how long it is kept")
		return
	}
	startChatDownload(w, format, name)
	s.streamChat(w, format, plan, true)
}

// streamChat writes a plan in a format. By now the status is committed, so a
// failure can only be logged: the client sees a truncated file and its own
// disconnect is the usual cause.
func (s *Server) streamChat(rw http.ResponseWriter, format string, plan chatexport.Plan, withOffset bool) {
	w := deadlineWriter{w: rw, rc: http.NewResponseController(rw)}
	var err error
	switch format {
	case "vtt":
		err = chatexport.WriteVTT(w, plan)
	case "csv":
		err = chatexport.WriteCSV(w, plan, withOffset)
	case "ndjson":
		err = chatexport.WriteNDJSON(w, plan)
	default:
		err = chatexport.WriteJSON(w, plan)
	}
	if err != nil {
		s.log.Debug("chat export interrupted", "format", format, "err", err)
	}
}

// chatWriteTimeout is how long one flush of an export may take. A server-wide
// write timeout would cut a long stream off; this renews the deadline before
// every write, so only a client that stops reading is cut off.
const chatWriteTimeout = 30 * time.Second

// deadlineWriter sets a fresh write deadline before each write, as the events
// stream does. A ResponseWriter that cannot (a test recorder) is written as is.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (d deadlineWriter) Write(p []byte) (int, error) {
	_ = d.rc.SetWriteDeadline(time.Now().Add(chatWriteTimeout))
	return d.w.Write(p)
}

// parseChatInstant reads a time as RFC 3339 or Unix milliseconds.
func parseChatInstant(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, true
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms), true
	}
	return time.Time{}, false
}

// chatPlatformParam accepts a platform id: lower-case letters, digits, '-' and
// '_'. It is only ever a bound query value, so this is hygiene, not the defence.
func chatPlatformParam(raw string) (db.Platform, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 32 {
		return "", false
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false
		}
	}
	return db.Platform(raw), true
}

// handleChatExport streams any window of stored chat. It reads the chat table,
// so it can only return what the chat history setting has not yet purged; the
// JSON header says whether the window was complete (csv and ndjson carry no
// header, so an analyst who needs that fact asks for json).
func (s *Server) handleChatExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" && format != "ndjson" {
		writeError(w, http.StatusBadRequest, "format must be json, csv or ndjson")
		return
	}
	from, ok := parseChatInstant(q.Get("from"))
	if !ok {
		writeError(w, http.StatusBadRequest, "from is required: an RFC 3339 time or Unix milliseconds")
		return
	}
	to := time.Now()
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		if to, ok = parseChatInstant(raw); !ok {
			writeError(w, http.StatusBadRequest, "to must be an RFC 3339 time or Unix milliseconds")
			return
		}
	}
	if !to.After(from) {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	platform, ok := chatPlatformParam(q.Get("platform"))
	if !ok {
		writeError(w, http.StatusBadRequest, "platform is not a platform id")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "chat storage is unavailable")
		return
	}

	plan, err := chatexport.PlanRange(s.store, db.ChatRange{
		FromMS: from.UnixMilli(), ToMS: to.UnixMilli(), Platform: platform,
	}, time.Now())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	name := fmt.Sprintf("polyemesis-chat-%s-%s.%s",
		from.UTC().Format("20060102T150405Z"), to.UTC().Format("20060102T150405Z"), format)
	startChatDownload(w, format, name)
	s.streamChat(w, format, plan, false)
}
