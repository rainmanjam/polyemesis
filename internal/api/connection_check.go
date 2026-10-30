package api

import (
	"net/http"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// TEST CONNECTION: what the token that made this request is, and may do.
//
// A pairing plugin (a Stream Deck inspector, a script) asks this once after the
// operator pastes a server URL and token, so it can show the install's answer
// before any key is relied on. It answers for the presented token only. There
// is no id in the path and no list, so one token cannot learn another's name.
//
// It is a GET, so a read-scoped token can ask. It carries no secret: the
// prefix is the same eight characters the token list already shows.
//
// Warnings are not written here as prose. adminScope says the token can do
// everything the operator can, and the client chooses the sentence. Keeping
// English out of the API is what lets the UI translate it.
//
// lastUsedAt is the value BEFORE this request (see db.APIToken
// PreviousLastUsedAt). There is no server clock in the answer: the body must
// not change between two identical requests, and a client that wants the time
// has its own.

// connectionCheckBody is the answer.
type connectionCheckBody struct {
	// Name is the label the operator gave the token.
	Name string `json:"name"`
	// Prefix is the first characters of the token, for matching against the
	// token list. Never the secret.
	Prefix string `json:"prefix"`
	// Scope is "read" or "admin" for a token, and "session" for a signed-in
	// browser, which is never a pairing credential.
	Scope string `json:"scope"`
	// AdminScope is true when the token can change anything the operator can.
	AdminScope bool `json:"adminScope"`
	// LastUsedAt is the token's previous use, omitted when it has never been
	// used.
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func (s *Server) handleConnectionCheck(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	body := connectionCheckBody{}
	if p.token == nil {
		// A browser session. It is not a pairing credential, and it can do
		// everything the operator can, so it is reported as the admin it is.
		body.Scope = "session"
		body.AdminScope = true
		writeJSON(w, http.StatusOK, body)
		return
	}
	body.Name = p.token.Name
	body.Prefix = p.token.Prefix
	body.Scope = p.token.Scope
	body.AdminScope = p.token.Scope == db.ScopeAdmin
	if !p.token.PreviousLastUsedAt.IsZero() {
		prev := p.token.PreviousLastUsedAt
		body.LastUsedAt = &prev
	}
	writeJSON(w, http.StatusOK, body)
}
