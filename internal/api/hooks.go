package api

import (
	"net/http"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/hooks"
)

// The HTTP surface for lifecycle webhooks.
//
// Shaped after the alert-rule handlers next door, deliberately: the two are
// different subsystems but they present the same operator problem -- an
// endpoint URL whose path is a credential -- and a hook that behaved
// differently about it would be a second thing to get right.

// hookRequest is a PATCH-shaped body: every field is a pointer, and an omitted
// one leaves the stored value alone.
//
// It exists instead of decoding straight into hooks.Hook because of the two
// secrets. A Hook marshals a MASKED url and no secret at all, so the only way a
// client can change either is a field the API defines itself.
type hookRequest struct {
	Name           *string          `json:"name"`
	Enabled        *bool            `json:"enabled"`
	URL            *string          `json:"url"`
	Secret         *string          `json:"secret"`
	Triggers       *[]hooks.Trigger `json:"triggers"`
	TimeoutSeconds *int             `json:"timeoutSeconds"`
	MaxAttempts    *int             `json:"maxAttempts"`
}

func (q hookRequest) applyTo(h hooks.Hook) hooks.Hook {
	if q.Name != nil {
		h.Name = *q.Name
	}
	if q.Enabled != nil {
		h.Enabled = *q.Enabled
	}
	if q.URL != nil {
		// The client was shown "https://host/[redacted]" and every form hands
		// it back untouched, because the field it renders is the only URL it
		// has. Storing that would point the hook at a URL that has never
		// existed and stop it firing silently, so a value still carrying the
		// mask means "unchanged".
		if u := strings.TrimSpace(*q.URL); !strings.Contains(u, alerts.Mask) {
			h.URL = u
		}
	}
	if q.Secret != nil {
		// Empty means unchanged, handled in db.UpdateHook: the UI never renders
		// the secret, so every edit form submits an empty one.
		h.Secret = strings.TrimSpace(*q.Secret)
	}
	if q.Triggers != nil {
		h.Triggers = append([]hooks.Trigger(nil), *q.Triggers...)
	}
	if q.TimeoutSeconds != nil {
		h.TimeoutSeconds = *q.TimeoutSeconds
	}
	if q.MaxAttempts != nil {
		h.MaxAttempts = *q.MaxAttempts
	}
	return h
}

func (s *Server) handleListHooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListHooks(s.box)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleGetHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	h, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleCreateHook stores a hook and returns its signing secret ONCE.
//
// The plaintext appears in exactly this response and nowhere else, matching the
// API-token handler (token_handlers.go:54). An operator pasting the key into
// their receiver needs it at this moment and never again, and a key that can be
// read back from a list endpoint is a key that leaks through every screenshot.
func (s *Server) handleCreateHook(w http.ResponseWriter, r *http.Request) {
	var req hookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// A new hook with no explicit enabled flag is on: somebody who just typed a
	// URL in wants it to fire.
	h := req.applyTo(hooks.Hook{Enabled: true}).Normalized()
	if err := h.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, plaintext, err := s.store.CreateHook(s.box, &h)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Encoded through the Hook's own MarshalJSON for the masked url and
	// hasSecret, with the plaintext added beside it rather than inside it, so
	// no future encoding of a Hook can accidentally carry it.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": out.ID, "hook": out,
		"secret": plaintext,
		"secretNote": "This signing key is shown once. Store it in your " +
			"receiver now; polyemesis cannot show it again.",
	})
}

func (s *Server) handleUpdateHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var req hookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The stored secret is not carried onto the request value: db.UpdateHook
	// re-reads it when the request left it empty. Copying it here would mean
	// two places that both have to remember, and one of them eventually will
	// not.
	updated := req.applyTo(*existing)
	updated.Secret = ""
	if req.Secret != nil {
		updated.Secret = strings.TrimSpace(*req.Secret)
	}
	updated = updated.Normalized()
	if err := updated.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.UpdateHook(s.box, &updated)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteHook(id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleTestHook sends one fully-formed delivery to the stored endpoint, right
// now, and reports what came back.
//
// This is the whole answer to "how does an operator test a hook without going
// live". It reads the hook from the store rather than from the body, so the URL
// under test is the URL that will really be used, and it returns the exact body
// and signature that were sent so the operator can check their verification
// code against real bytes rather than against the documentation.
func (s *Server) handleTestHook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	h, err := s.store.GetHook(s.box, id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.hooks == nil {
		writeError(w, http.StatusServiceUnavailable, "the hook dispatcher is not running")
		return
	}
	trigger := hooks.Trigger(r.URL.Query().Get("trigger"))
	res, err := s.hooks.Test(r.Context(), *h, trigger)
	if err != nil {
		// 502, not 500: the failure is the operator's endpoint, and the message
		// says which. Errors out of the dispatcher are already redacted.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleHookDeliveries returns one hook's recent attempts, so "did it fire, and
// what did my endpoint say" is answerable from the console.
func (s *Server) handleHookDeliveries(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if s.hooks == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.hooks.Deliveries(id))
}

// handleHooksMeta is the catalogue the hook editor builds its pickers from, so
// a new trigger is added in exactly one place.
func (s *Server) handleHooksMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"triggers":    hooks.AllTriggers(),
		"specVersion": hooks.SpecVersion,
		"headers": map[string]string{
			"signature": hooks.SignatureHeader,
			"timestamp": hooks.TimestampHeader,
			"trigger":   hooks.TriggerHeader,
			"delivery":  hooks.DeliveryHeader,
			"sequence":  hooks.SequenceHeader,
		},
		"bounds": map[string]int{
			"minTimeoutSeconds": hooks.MinTimeoutSeconds,
			"maxTimeoutSeconds": hooks.MaxTimeoutSeconds,
			"minAttempts":       hooks.MinAttempts,
			"maxAttempts":       hooks.MaxAttempts,
			"maxNameLen":        hooks.MaxHookNameLen,
			"maxUrlLen":         hooks.MaxURLLen,
		},
		"stats": s.hooks.Stats(),
	})
}
