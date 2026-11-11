package recording

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/chatexport"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// ============================================================================
// A RECORDING'S CHAT: <name>.chat.json and <name>.chat.vtt beside the master.
//
// Chat lives in a bounded replay buffer that the purge empties, so the durable
// copy is written here, once, when a segment is finished. The files are not
// indexed (no schema change), so they follow their master the way stems and
// transcripts do: Delete and retention remove them with it, a sweep removes the
// ones whose recording went missing, and offload carries them along.
//
// WHEN A SEGMENT IS FINISHED is read from its file, not from the index. The
// index's own notion of "live" always includes the newest segment, and Scan
// never measures that one, so the last file of every stream would wait for the
// next stream to start -- by which time the purge has taken its chat. A
// recorder writes constantly, so a file nobody has written to for chatSettle is
// finished or dead, and either way its chat is final.
//
// WHAT A SIDECAR IS NOT WRITTEN FOR: a window that holds no chat. On the first
// boot after this shipped every old recording would otherwise receive an empty
// file that reads as "nobody chatted", when the truth is "the purge got there
// first".
// ============================================================================

// chatSettle is how long a segment's file must be untouched before its chat is
// taken as final. Well above the chat hub's flush interval, so every message of
// the segment has reached the table.
const chatSettle = 2 * time.Minute

// chatSeam is the largest gap or overlap between one segment's computed end and
// the next segment's start that is closed up rather than left. The recorder
// stamps each file's start in whole seconds, so consecutive windows built from
// start+duration disagree by up to a second; closing the seam gives every
// message to exactly one file instead of none or two.
const chatSeam = 5 * time.Second

// ChatWindow is the message-time window [startMS, endMS) of the chat that
// belongs to rec, and where its end came from. all is every indexed recording,
// used to find the next segment of the same programme. mtime is the file's last
// write, the end of a segment that was never measured.
//
// The window is the one the highlight scanner reads for a recording part
// (StartedAt .. StartedAt+Duration, on the at_ms clock), plus the seam rule
// above. A programme recorded as several files is therefore several windows
// that tile the session. Two programmes recording at once have OVERLAPPING
// windows, and chat_messages carries no programme, so each file's sidecar holds
// all the chat of that period -- exactly what the highlight scanner does.
func ChatWindow(rec db.Recording, all []db.Recording, mtime time.Time) (startMS, endMS int64, endSource string, ok bool) {
	startMS = rec.StartedAt.UnixMilli()
	if rec.DurationMS > 0 {
		endMS, endSource = startMS+rec.DurationMS, "probe"
	} else {
		endMS, endSource = mtime.UnixMilli(), "mtime"
	}
	if endMS <= startMS {
		return 0, 0, "", false
	}
	next := nextProgrammeStart(rec, all, startMS)
	if next != 0 {
		d := next - endMS
		if d < 0 {
			d = -d
		}
		if d <= chatSeam.Milliseconds() {
			endMS = next
		}
	}
	return startMS, endMS, endSource, true
}

// nextProgrammeStart is the earliest start, after startMS, of another recording
// of the same programme, or 0 when there is none.
func nextProgrammeStart(rec db.Recording, all []db.Recording, startMS int64) int64 {
	var next int64
	for _, o := range all {
		if o.ID == rec.ID || !sameProgramme(o.SourceID, rec.SourceID) {
			continue
		}
		if s := o.StartedAt.UnixMilli(); s > startMS && (next == 0 || s < next) {
			next = s
		}
	}
	return next
}

func sameProgramme(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ChatPlan describes the chat of one recording, built from the chat table. It
// is what the sidecar is written from and what the on-demand export serves when
// no sidecar exists.
func (m *Manager) ChatPlan(rec db.Recording) (chatexport.Plan, error) {
	all, err := m.store.ListRecordings()
	if err != nil {
		return chatexport.Plan{}, err
	}
	mtime := time.Now()
	if path, err := m.Resolve(rec.Filename); err == nil {
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
	}
	return m.chatPlan(rec, all, mtime)
}

func (m *Manager) chatPlan(rec db.Recording, all []db.Recording, mtime time.Time) (chatexport.Plan, error) {
	startMS, endMS, endSource, ok := ChatWindow(rec, all, mtime)
	if !ok {
		return chatexport.Plan{}, chatexport.ErrNotRetained
	}
	info := chatexport.RecordingInfo{
		File: rec.Filename, StartedAt: rec.StartedAt.UTC(), DurationMs: endMS - startMS,
		EndSource: endSource, StartPrecision: "1s",
	}
	return chatexport.PlanRecording(m.store, info, startMS, endMS, time.Now())
}

// ChatSidecar returns the path of a recording's saved chat file (vtt selects
// the subtitle one) when it exists.
func (m *Manager) ChatSidecar(filename string, vtt bool) (string, bool) {
	jsonName, vttName := chatexport.SidecarNames(filename)
	name := jsonName
	if vtt {
		name = vttName
	}
	path, err := m.Resolve(name)
	if err != nil {
		return "", false
	}
	// Lstat, so a symlink planted in the directory is neither served nor
	// uploaded: only a regular file this package wrote counts.
	fi, err := os.Lstat(path)
	return path, err == nil && fi.Mode().IsRegular()
}

// chatSidecarInfo is the JSON sidecar's file info, when it exists.
func (m *Manager) chatSidecarInfo(filename string) (os.FileInfo, bool) {
	path, ok := m.ChatSidecar(filename, false)
	if !ok {
		return nil, false
	}
	fi, err := os.Lstat(path)
	return fi, err == nil
}

// savedChatHeader reads the header of the saved JSON sidecar: the part before
// its "messages" array, which WriteJSON puts on the first line.
func (m *Manager) savedChatHeader(filename string) (chatexport.Header, bool) {
	var h chatexport.Header
	path, ok := m.ChatSidecar(filename, false)
	if !ok {
		return h, false
	}
	f, err := os.Open(path)
	if err != nil {
		return h, false
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return h, false
	}
	line = bytes.TrimSuffix(bytes.TrimRight(line, "\r\n"), []byte(",\"messages\":["))
	if json.Unmarshal(append(line, '}'), &h) != nil {
		return h, false
	}
	return h, true
}

// HasChatSidecar reports whether chat was saved with the recording. The JSON
// file is written last, so its presence means the pair is complete.
func (m *Manager) HasChatSidecar(filename string) bool {
	_, ok := m.ChatSidecar(filename, false)
	return ok
}

// writeChatSidecars saves the chat of every finished segment that has none.
// Best effort: a failure is logged and tried again on the next scan.
func (m *Manager) writeChatSidecars() bool {
	recs, err := m.store.ListRecordings()
	if err != nil {
		m.log.Warn("cannot list recordings for chat sidecars", "err", err)
		return false
	}
	now := time.Now()
	changed := false
	for _, rec := range recs {
		path, err := m.Resolve(rec.Filename)
		if err != nil {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < chatSettle {
			continue
		}
		if m.chatSettled(rec.Filename, fi.ModTime()) {
			continue
		}
		// A sidecar older than its master is stale: the file was written to
		// again after the chat was saved (a recorder that sat idle for longer
		// than chatSettle and then carried on), so it is saved again.
		if sc, ok := m.chatSidecarInfo(rec.Filename); ok && !sc.ModTime().Before(fi.ModTime()) {
			m.markChatSettled(rec.Filename, fi.ModTime())
			continue
		}
		plan, err := m.chatPlan(rec, recs, fi.ModTime())
		if err != nil {
			m.log.Warn("cannot read chat for a finished recording", "file", rec.Filename, "err", err)
			continue
		}
		// NEVER DOWNGRADE A SAVED FILE. The purge may have taken part of the
		// window since it was written (the archive job bumps a master's mtime
		// hours later, and the keep floor still leaves some rows), so a rewrite
		// from the table could replace a complete file with a tail. A rewrite
		// must hold at least as much as what is there.
		if prev, ok := m.savedChatHeader(rec.Filename); ok && !plan.Header.Complete &&
			(prev.Complete || prev.MessageCount > plan.Header.MessageCount) {
			m.markChatSettled(rec.Filename, fi.ModTime())
			continue
		}
		if plan.Header.MessageCount == 0 {
			// Nothing to keep, and asking again every 30 s would be a query per
			// quiet recording for ever. A restart asks once more.
			m.markChatSettled(rec.Filename, fi.ModTime())
			continue
		}
		if err := m.saveChat(rec.Filename, plan); err != nil {
			m.log.Warn("cannot save a recording's chat", "file", rec.Filename, "err", err)
			continue
		}
		m.markChatSettled(rec.Filename, fi.ModTime())
		m.log.Info("saved chat with recording", "file", rec.Filename,
			"messages", plan.Header.MessageCount, "complete", plan.Header.Complete)
		changed = true
	}
	return changed
}

// chatSettled reports whether the chat of name needs no more attention as of
// the file's last write. Keyed on the write time, so a file written to again
// is looked at again.
func (m *Manager) chatSettled(name string, mtime time.Time) bool {
	m.chatMu.Lock()
	defer m.chatMu.Unlock()
	at, ok := m.chatDone[name]
	return ok && at.Equal(mtime)
}

func (m *Manager) markChatSettled(name string, mtime time.Time) {
	m.chatMu.Lock()
	defer m.chatMu.Unlock()
	if m.chatDone == nil {
		m.chatDone = map[string]time.Time{}
	}
	m.chatDone[name] = mtime
}

// saveChat writes the VTT, then the JSON. Each goes to a dot-prefixed temporary
// file in the same directory and is renamed into place, so a reader never sees
// half a file and two managers scanning this shared directory cannot corrupt
// one another's write: the loser's rename replaces an identical file. The JSON
// goes last because its presence is what HasChatSidecar and the offload read.
func (m *Manager) saveChat(filename string, plan chatexport.Plan) error {
	jsonName, vttName := chatexport.SidecarNames(filename)
	if err := m.writeAtomic(vttName, func(f *os.File) error { return chatexport.WriteVTT(f, plan) }); err != nil {
		return err
	}
	return m.writeAtomic(jsonName, func(f *os.File) error { return chatexport.WriteJSON(f, plan) })
}

func (m *Manager) writeAtomic(name string, write func(*os.File) error) error {
	dest, err := m.Resolve(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.dir, "."+name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	err = write(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	// Owner-only, like the chat table it is copied from: a sidecar names every
	// viewer who spoke, and it is served through the API, not read off disk.
	if err == nil {
		err = os.Chmod(tmpName, 0o600)
	}
	if err == nil {
		err = os.Rename(tmpName, dest)
	}
	if err != nil {
		_ = os.Remove(tmpName)
	}
	return err
}

// removeChatSidecars deletes a recording's saved chat. Best effort, like
// removeDerived: the master is already gone.
//
// Two masters can share a stem (x.mkv and x.ts) and so share sidecars; they stay
// while any other indexed recording or master on disk has the same stem.
func (m *Manager) removeChatSidecars(filename string) {
	if m.stemShared(filename) {
		return
	}
	jsonName, vttName := chatexport.SidecarNames(filename)
	for _, name := range []string{jsonName, vttName} {
		path, err := m.Resolve(name)
		if err != nil {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			m.log.Warn("cannot remove a deleted recording's chat", "file", name, "err", err)
		}
	}
	m.chatMu.Lock()
	delete(m.chatDone, filename)
	m.chatMu.Unlock()
}

// stemOf is a recording's name without its extension.
func stemOf(name string) string {
	base := filepath.Base(name)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// stemShared reports whether another recording, indexed or merely on disk,
// has the same stem as filename.
func (m *Manager) stemShared(filename string) bool {
	stem := stemOf(filename)
	if recs, err := m.store.ListRecordings(); err == nil {
		for _, r := range recs {
			if r.Filename != filename && stemOf(r.Filename) == stem {
				return true
			}
		}
	}
	if entries, err := os.ReadDir(m.dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && isRecording(e.Name()) && e.Name() != filepath.Base(filename) && stemOf(e.Name()) == stem {
				return true
			}
		}
	}
	return false
}

// staleChatTemp is how old a leftover temporary file must be before the sweep
// removes it: far longer than any write, so a live write is never touched.
const staleChatTemp = time.Hour

// chatTempName is what writeAtomic's os.CreateTemp leaves: "." + the sidecar's
// name + "." + digits + ".tmp".
var chatTempName = regexp.MustCompile(`^\..+(` + regexp.QuoteMeta(chatexport.JSONSuffix) + `|` +
	regexp.QuoteMeta(chatexport.VTTSuffix) + `)\.[0-9]+\.tmp$`)

// sweepChatSidecars removes chat files whose recording is gone, and temporary
// files a crashed write left. known is every indexed recording; like the other
// sweeps it does nothing on an empty index, because "we cannot tell" must not
// read as "everything is an orphan".
func (m *Manager) sweepChatSidecars(known map[string]bool) int {
	if len(known) == 0 {
		return 0
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0
	}
	bases := chatSidecarBases(known, entries)
	removed := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !orphanChatFile(e, bases) {
			continue
		}
		name := e.Name()
		if err := os.Remove(filepath.Join(m.dir, name)); err != nil {
			m.log.Warn("cannot remove an orphaned chat file", "file", name, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		m.log.Info("removed chat files for recordings that are gone", "count", removed)
	}
	return removed
}

// chatSidecarBases is the set of recording stems whose chat is still wanted:
// every indexed recording, and every master on disk.
func chatSidecarBases(known map[string]bool, entries []os.DirEntry) map[string]bool {
	bases := make(map[string]bool, len(known))
	for name := range known {
		bases[stemOf(name)] = true
	}
	// A master on disk is a survivor too, indexed yet or not (as companions.go
	// does): its chat is somebody's, and the scan simply has not got to it.
	for _, e := range entries {
		if !e.IsDir() && isRecording(e.Name()) {
			bases[stemOf(e.Name())] = true
		}
	}
	return bases
}

// orphanChatFile reports whether a regular file in the recordings directory is
// chat that nothing wants: a temporary left by a crashed write that is stale, or
// a sidecar whose recording is gone.
func orphanChatFile(e os.DirEntry, bases map[string]bool) bool {
	name := e.Name()
	if chatTempName.MatchString(name) {
		info, err := e.Info()
		return err == nil && time.Since(info.ModTime()) >= staleChatTemp
	}
	base, ok := chatexport.SidecarBase(name)
	return ok && !bases[base]
}

// chatHoldSlack is how long after its last write a recording's chat is still
// held back from the purge: the settle window plus a few scans, so the sidecar
// is written before the rows can go.
const chatHoldSlack = chatSettle + 90*time.Second

// chatHoldWindow bounds which recordings are considered: a segment is at most
// 24 hours long.
const chatHoldWindow = 25 * time.Hour

// ChatHold is the earliest start among the recordings whose chat is not yet
// safely in a sidecar: those being written, or finished within the last few
// minutes. The chat purge does not pass it (bounded by the hub), so a segment
// longer than the chat retention still ends up with complete chat.
func (m *Manager) ChatHold(now time.Time) (time.Time, bool) {
	recs, err := m.store.ListRecordings()
	if err != nil {
		return time.Time{}, false
	}
	var first time.Time
	for _, r := range recs {
		if r.StartedAt.Before(now.Add(-chatHoldWindow)) {
			continue
		}
		path, err := m.Resolve(r.Filename)
		if err != nil {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || now.Sub(fi.ModTime()) >= chatHoldSlack {
			continue
		}
		if first.IsZero() || r.StartedAt.Before(first) {
			first = r.StartedAt
		}
	}
	return first, !first.IsZero()
}
