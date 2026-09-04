package api

import (
	"fmt"
	"net/http"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// requireSession rejects token-authenticated callers, and it is MIDDLEWARE
// rather than a call each handler makes because the difference is what #140
// turned out to be.
//
// It began as `if !requireSession(w, r) { return }` at the top of the three
// handlers below. That worked for those three and enforced nothing anywhere
// else, so when the media routes were added with a comment stating that a token
// could not reach them, the comment was the only thing that said so: the routes
// carried requireAuth and requireCSRF, and requireCSRF passes a token principal
// through deliberately, because nothing attaches an Authorization header on its
// own. A token-only POST therefore uploaded a file, for as long as the claim
// stood in SECURITY.md unchallenged by any code.
//
// A per-handler check would have fixed those two handlers and left the next
// session-only route to remember for itself. Mounted on a router group instead,
// the route table IS the claim -- a handler is session-only exactly when it is
// registered inside the group, and forgetting to add it there denies nothing
// that was already denied, while forgetting a per-handler call silently permits.
//
// What is being protected differs by route and the reason does not. A leaked
// token already grants everything the admin can do, but minting further tokens
// would make revocation useless -- the operator deletes the one they know about
// while the holder has quietly issued three more -- and writing arbitrary bytes
// to the server's disk is not something a credential meant for automation
// should reach at all.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if p.token != nil {
			writeError(w, http.StatusForbidden, "API tokens cannot reach this endpoint; sign in to do this")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		// Scope is optional, and omitting it mints a read-only token.
		//
		// The weaker default is the point of #104. A caller that does not
		// mention scopes -- an older UI build, a curl line copied from a blog
		// post, a script written before this field existed -- gets the
		// credential that can only read. Defaulting to admin would mean the
		// feature protected nobody who did not already know about it.
		Scope string `json:"scope"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Scope != "" && !db.ValidScope(req.Scope) {
		writeError(w, http.StatusBadRequest, "unknown token scope; use \"read\" or \"admin\"")
		return
	}

	token, plaintext, err := s.store.CreateAPIToken(req.Name, req.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("api token created", "name", token.Name, "prefix", token.Prefix, "scope", token.Scope)
	// Loud, because this is the credential that survives the response to a
	// compromise. Minting is already behind the password rather than behind a
	// token -- see requireSession -- so somebody reaching here is somebody who
	// has the password, and a token they mint keeps working after the password
	// is changed. The alert carries the name and not the prefix; see
	// auditAPITokenCreated.
	s.publishAudit(auditAPITokenCreated(token.Name, token.Scope, s.clientIP(r)))

	// The only response that ever carries the plaintext. Nothing stores it,
	// so a client that drops this field has to start over.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"plaintext": plaintext,
	})
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	// Read the name BEFORE the row goes, because the created event carries a
	// name and an operator pairs the two by eye. An id alone would make them
	// cross-reference a list the deletion has already changed. A failed lookup
	// is not worth failing the revocation over -- the event degrades to the id.
	name := fmt.Sprintf("#%d", id)
	if tokens, err := s.store.ListAPITokens(); err == nil {
		for _, t := range tokens {
			if t.ID == id {
				name = t.Name
				break
			}
		}
	}
	if err := s.store.DeleteAPIToken(id); err != nil {
		writeStoreError(w, err)
		return
	}
	// The socket half of revocation (#159). Deleting the row stops the token
	// authenticating the NEXT request, and that used to be the whole of it: a
	// /ws socket resolves its principal once, at upgrade, and then never again,
	// so a token revoked during an incident went on feeding a live telemetry
	// stream to whoever held it for as long as they kept the connection open.
	// This is the only writer of the revoked set, and it is the reason
	// DeleteAPIToken having exactly one call site was worth checking: a second
	// deletion path that did not do this would be a silent hole.
	s.log.Info("api token revoked", "id", id)
	s.publishAudit(auditAPITokenRevoked(name, s.clientIP(r)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
