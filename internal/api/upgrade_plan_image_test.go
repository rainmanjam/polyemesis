package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/upgrade"
)

// A container built from source by the shipped docker-compose.yml runs version
// "compose". Its service has `build:` and no `image:`, so `docker compose pull`
// skips it and `up -d` restarts the same image: the plan must not offer that
// as the upgrade.
func TestComposeBuiltPlanDoesNotOfferANoOpPull(t *testing.T) {
	s, _, _ := testServer(t, config.Config{})
	s.upgradeMethod = upgrade.MethodDocker
	stubOnAir(t, engine.OnAir{})
	for _, running := range []string{"compose", "docker", "v0.9.0-12-gabcdef1"} {
		s.version = running
		p := s.upgradePlan("v0.10.0")
		if !strings.Contains(p.Command, "--build") {
			t.Errorf("running %q: the command does not rebuild, so it upgrades nothing: %q", running, p.Command)
		}
	}
}
