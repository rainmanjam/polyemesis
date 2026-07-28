package api

import "net/http"

// requireSession rejects token-authenticated callers.
//
// A leaked token already grants everything the admin can do, but letting it
// mint further tokens would make revocation useless: the operator deletes the
// one they know about while the holder has quietly issued three more. Minting
// stays behind the password.
func requireSession(w http.ResponseWriter, r *http.Request) bool {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return false
	}
	if p.token != nil {
		writeError(w, http.StatusForbidden, "API tokens cannot manage API tokens; sign in to do this")
		return false
	}
	return true
}

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	tokens, err := s.store.ListAPITokens()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	token, plaintext, err := s.store.CreateAPIToken(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("api token created", "name", token.Name, "prefix", token.Prefix)

	// The only response that ever carries the plaintext. Nothing stores it,
	// so a client that drops this field has to start over.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     token,
		"plaintext": plaintext,
	})
}

func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAPIToken(id); err != nil {
		writeStoreError(w, err)
		return
	}
	s.log.Info("api token revoked", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
