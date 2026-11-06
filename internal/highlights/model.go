package highlights

import (
	"context"
	"log/slog"
	"time"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/secrets"
)

// modelTimeout bounds the one scoring call a scan makes. Longer than
// moderation's four seconds: it is a dozen excerpts, not one message, and
// nobody is waiting on it in chat.
const modelTimeout = 60 * time.Second

// ModelFrom returns the scan's model factory: the operator's PRIMARY
// automod connector -- its endpoint, model and key -- when the separate
// "Use AI for highlight scoring" switch is on and the connector speaks the
// OpenAI-compatible protocol, charged to budget -- the scan's own, so a post-pass over a
// recording can never spend the allowance live moderation runs on.
//
// It lives with the scan, not in the HTTP layer: the worker is wired in
// cmd/polyemesis and the settings handler asks ModelConfig the same question.
func ModelFrom(store *db.DB, box *secrets.Box, budget *automod.Budget, log *slog.Logger) func() (Completer, string) {
	return func() (Completer, string) {
		cfg, why := ModelConfig(store, box, log)
		if why != "" {
			return nil, why
		}
		return func(ctx context.Context, system, user string) (string, error) {
			return automod.Complete(ctx, cfg, budget, system, user)
		}, ""
	}
}

// ModelConfig is the connector the scan would use, or why there is
// none. The reason is fixed text: it reaches the review queue, and the
// endpoint (which may carry a key in its query string) never does.
func ModelConfig(store *db.DB, box *secrets.Box, log *slog.Logger) (automod.ModelConfig, string) {
	if store == nil {
		return automod.ModelConfig{}, "there is no settings store"
	}
	settings, err := store.GetSettings()
	if err != nil {
		return automod.ModelConfig{}, "the automod settings could not be read"
	}
	conn := settings.Automod.Model
	switch {
	case !settings.Automod.HighlightScoring:
		// Its own consent (owner decision D): the Automod model block being on
		// for chat says nothing about sending what was said on stream.
		return automod.ModelConfig{}, "AI highlight scoring is switched off in Automod settings"
	case automod.ProtocolOf(conn) == automod.ProtocolJev:
		return automod.ModelConfig{}, "the Automod model uses the Jev protocol, which answers moderation questions only"
	case conn.Endpoint == "":
		return automod.ModelConfig{}, "the Automod model has no endpoint"
	}
	cfg, _ := automod.ModelConfigFor(conn, settings.Automod.Model)
	key, binding, err := store.GetAutomodKeyBound(box, string(automod.SlotPrimary))
	if err != nil {
		if log != nil {
			log.Warn("highlight scoring: automod key unreadable; scoring without it", "err", err)
		}
	}
	cfg.APIKey, cfg.KeyBinding = key, binding
	cfg.Timeout = modelTimeout
	return cfg, ""
}
