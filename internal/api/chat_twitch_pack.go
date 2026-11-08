package api

// The recommended-protection pack on Twitch's own filters (spec section 7.4).
//
// These are resources under the existing /chat/twitch/{resource} routes, so the
// route ledger, the read-scope rule and the wire contract see no new pattern:
//
//	GET    pack-terms            counts: what would be pushed, what polyemesis has added
//	POST   pack-terms-sync       push the pack's severe hate terms; reconciles; resumable
//	DELETE pack-terms            remove exactly the terms polyemesis added
//	PUT    pack-automod-level    raise Twitch AutoMod to at least level 1 or 2
//	POST   pack-protected-names  refresh impersonation's protected names from Twitch roles
//
// Every one is an explicit click. Nothing here runs on its own, because each
// changes the channel's own settings. None of them returns a term, and the
// record of what was added holds a hash per term, not the term: the pack is a
// slur list and the console only needs how many.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/automod/pack"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// packToolFn is a Twitch pack tool; unlike toolFn it can reach the server.
type packToolFn func(s *Server, r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error)

var twitchPackTools = map[string]packToolFn{
	http.MethodGet + " pack-terms":            (*Server).packTermsStatus,
	http.MethodPost + " pack-terms-sync":      (*Server).packTermsSync,
	http.MethodDelete + " pack-terms":         (*Server).packTermsRemove,
	http.MethodPut + " pack-automod-level":    (*Server).packAutomodLevel,
	http.MethodPost + " pack-protected-names": (*Server).packProtectedNames,
}

// servePackTool runs a pack tool, reporting whether the request was one.
func (s *Server) servePackTool(w http.ResponseWriter, r *http.Request) bool {
	resource := chi.URLParam(r, "resource")
	fn, ok := twitchPackTools[r.Method+" "+resource]
	if !ok {
		return false
	}
	if !s.requireChat(w) {
		return true
	}
	var b twitchToolBody
	if (r.Method == http.MethodPost || r.Method == http.MethodPut) && !decodeJSON(w, r, &b) {
		return true
	}
	var out any
	err := s.chat.WithTwitchTools(r.Context(), strings.TrimSpace(r.URL.Query().Get("account")), twitchLongToolTimeout,
		func(ctx context.Context, tt chat.TwitchTools) (err error) {
			out, err = fn(s, r.WithContext(ctx), tt, b)
			return err
		})
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case r.Method == http.MethodGet:
			status = http.StatusBadGateway
		case errors.Is(err, errPackSyncBusy):
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return true
	}
	writeJSON(w, http.StatusOK, out)
	return true
}

var errPackSyncBusy = errors.New("another Twitch pack action is already running; wait for it to finish")

// packTwitchTerms is what the pack asks Twitch to block: the severe hate terms
// of the built-in list plus the operator's own added severe hate terms, minus
// the ones they allowed, each once. Nothing when hate & slurs is switched off.
//
// Twitch does its own matching (whole words, with * as a wildcard at an end),
// so the pack's lowercase form is sent as it is. The pack's exceptions are a
// polyemesis-side idea Twitch cannot be given.
func packTwitchTerms(p db.AutomodProtection) ([]string, error) {
	if c, ok := p.Categories[pack.CategoryHate]; ok && c.Enabled != nil && !*c.Enabled {
		return nil, nil
	}
	pk, err := pack.Default()
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(p.AllowTerms))
	for _, a := range p.AllowTerms {
		allowed[pack.Fold(a)] = true
	}
	seen := map[string]bool{}
	var out []string
	put := func(term string) {
		term = strings.TrimSpace(term)
		key := strings.ToLower(term)
		if term == "" || seen[key] || allowed[pack.Fold(term)] {
			return
		}
		seen[key] = true
		out = append(out, term)
	}
	for _, e := range pk.Entries {
		if e.Category == pack.CategoryHate && e.Tier == pack.TierSevere {
			put(e.Term)
		}
	}
	for _, a := range p.AddTerms {
		if a.Category == pack.CategoryHate && (a.Tier == pack.TierSevere || a.Tier == "") && utf8.RuneCountInString(a.Term) > 0 {
			put(a.Term)
		}
	}
	sort.Strings(out)
	return out, nil
}

// packStatus is GET pack-terms.
type packStatus struct {
	// Eligible is how many terms a sync would send, Recorded how many Twitch
	// terms polyemesis added and can remove.
	Eligible int `json:"eligible"`
	Recorded int `json:"recorded"`
	// PushedPackVersion is the pack version last fully pushed; PackVersion the
	// one this build carries.
	PushedPackVersion int `json:"pushedPackVersion"`
	PackVersion       int `json:"packVersion"`
	// On is whether recommended protection is on, which a push requires.
	On bool `json:"on"`
}

func (s *Server) packTermsStatus(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
	set, err := s.store.GetSettings()
	if err != nil {
		return nil, err
	}
	terms, err := packTwitchTerms(set.Automod.Protection)
	if err != nil {
		return nil, err
	}
	n := set.Automod.Protection.Native.Twitch.Channel(tt.ChannelID())
	return packStatus{
		Eligible: len(terms), Recorded: len(n.AddedTermIDs), PushedPackVersion: n.PushedPackVersion,
		PackVersion: db.DefaultProtectionPackVersion, On: protectionOn(set.Automod),
	}, nil
}

// errNotRecorded wraps a failure to write the record, saying what it means: the
// terms are on Twitch and polyemesis does not know them.
func errNotRecorded(res chat.TrackedTermSync, err error) error {
	return fmt.Errorf("%d terms were added to Twitch but could not be recorded, so polyemesis cannot remove them later: %w",
		len(res.AddedIDs), err)
}

// recordNative stores what a sync or removal really did, under the channel it
// was done to. It runs on every exit, error included: a term Twitch accepted
// and polyemesis forgot is one it can never remove. It writes only the record
// (UpdateProtectionNative), so nothing else in the settings can stop it.
func (s *Server) recordNative(channel string, res chat.TrackedTermSync, complete bool) error {
	if len(res.AddedIDs) == 0 && len(res.RemovedIDs) == 0 && !complete {
		return nil
	}
	err := s.store.UpdateProtectionNative(func(n *db.ProtectionNative) {
		c := n.Twitch.Channel(channel)
		ids := make(map[string]string, len(c.AddedTermIDs)+len(res.AddedIDs))
		for id, key := range c.AddedTermIDs {
			ids[id] = key
		}
		for id, key := range res.AddedIDs {
			ids[id] = key
		}
		for _, id := range res.RemovedIDs {
			delete(ids, id)
		}
		c.AddedTermIDs = ids
		if complete {
			c.PushedPackVersion = db.DefaultProtectionPackVersion
		}
		n.Twitch.Put(c)
	})
	if err != nil && len(res.AddedIDs) > 0 {
		return errNotRecorded(res, err)
	}
	return err
}

func (s *Server) packTermsSync(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
	if !s.packMu.TryLock() {
		return nil, errPackSyncBusy
	}
	defer s.packMu.Unlock()
	set, err := s.store.GetSettings()
	if err != nil {
		return nil, err
	}
	if !protectionOn(set.Automod) {
		return nil, errors.New("turn on recommended protection first; there are no pack terms to copy while it is off")
	}
	want, err := packTwitchTerms(set.Automod.Protection)
	if err != nil {
		return nil, err
	}
	channel := tt.ChannelID()
	rec := set.Automod.Protection.Native.Twitch.Channel(channel).AddedTermIDs
	res, syncErr := tt.SyncTrackedTerms(r.Context(), want, rec, chat.TwitchPackBatch)
	if err := s.recordNative(channel, res, syncErr == nil && res.Done); err != nil {
		return nil, err
	}
	if syncErr != nil {
		return nil, syncErr
	}
	return res, nil
}

func (s *Server) packTermsRemove(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
	if !s.packMu.TryLock() {
		return nil, errPackSyncBusy
	}
	defer s.packMu.Unlock()
	set, err := s.store.GetSettings()
	if err != nil {
		return nil, err
	}
	channel := tt.ChannelID()
	res, rmErr := tt.RemoveTrackedTerms(r.Context(), set.Automod.Protection.Native.Twitch.Channel(channel).AddedTermIDs)
	if err := s.recordNative(channel, res, false); err != nil {
		return nil, err
	}
	if rmErr == nil && res.Done {
		// Nothing of the pack is on this channel any more.
		if err := s.store.UpdateProtectionNative(func(n *db.ProtectionNative) {
			c := n.Twitch.Channel(channel)
			c.PushedPackVersion = 0
			n.Twitch.Put(c)
		}); err != nil {
			return nil, err
		}
	}
	if rmErr != nil {
		return nil, rmErr
	}
	return res, nil
}

// packLevelAnswer is PUT pack-automod-level: the levels either side, so the
// console can say "already stricter" and offer the way back.
type packLevelAnswer struct {
	Before chat.AutoModSettings `json:"before"`
	After  chat.AutoModSettings `json:"after"`
}

func (s *Server) packAutomodLevel(r *http.Request, tt chat.TwitchTools, b twitchToolBody) (any, error) {
	before, after, err := tt.RaiseAutoModLevel(r.Context(), b.Level)
	if err != nil {
		return nil, err
	}
	return packLevelAnswer{Before: before, After: after}, nil
}

// packNamesAnswer is POST pack-protected-names.
type packNamesAnswer struct {
	Moderators int `json:"moderators"`
	VIPs       int `json:"vips"`
	// VIPNote is why VIPs could not be read, when they could not: that is a
	// Twitch requirement and never a reason to drop the moderators.
	VIPNote string `json:"vipNote,omitempty"`
}

// packProtectedNames teaches impersonation protection the channel's moderators
// and VIPs. Learned names are kept in memory, bounded, and not written to
// settings (a runtime write would race the operator's own save).
func (s *Server) packProtectedNames(r *http.Request, tt chat.TwitchTools, _ twitchToolBody) (any, error) {
	var out packNamesAnswer
	learn := func(hs []chat.RoleHolder) {
		if s.automodState == nil || s.automodState.Names == nil {
			return
		}
		for _, h := range hs {
			s.automodState.Names.Learn(h.Name)
			if h.Login != "" && !strings.EqualFold(h.Login, h.Name) {
				s.automodState.Names.Learn(h.Login)
			}
		}
	}
	mods, err := tt.ListRole(r.Context(), "moderator")
	if err != nil {
		return nil, err
	}
	learn(mods)
	out.Moderators = len(mods)
	vips, err := tt.ListRole(r.Context(), "vip")
	if err != nil {
		out.VIPNote = err.Error()
	} else {
		learn(vips)
		out.VIPs = len(vips)
	}
	return out, nil
}

// cloneNative copies the platform-term record, so a decode that merges into the
// stored map in place cannot change the copy.
func cloneNative(n db.ProtectionNative) db.ProtectionNative {
	out := n
	if n.Twitch.Channels != nil {
		out.Twitch.Channels = make([]db.ProtectionTwitchChannel, len(n.Twitch.Channels))
		for i, c := range n.Twitch.Channels {
			if c.AddedTermIDs != nil {
				ids := make(map[string]string, len(c.AddedTermIDs))
				for k, v := range c.AddedTermIDs {
					ids[k] = v
				}
				c.AddedTermIDs = ids
			}
			out.Twitch.Channels[i] = c
		}
	}
	return out
}
