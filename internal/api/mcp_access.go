package api

import (
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// MCP ACCESS IS OPT-IN, IN THREE LAYERS, AND EVERY ONE OF THEM IS ENFORCED HERE.
//
//  1. The switch (db.MCPAccess.Enabled, off on every install until an operator
//     turns it on). While it is off, /mcp is answered by the router's own
//     not-found handler, for every method and every caller -- the bytes an
//     unknown endpoint gets -- so a scanner cannot tell this build has MCP, and
//     a token cannot tell whether it is the switch or the token that is wrong.
//  2. The token. /mcp takes only an MCP-scope token (db.ScopeMCPRead,
//     db.ScopeMCPAdmin), and authenticate refuses an MCP token on every other
//     route. So the Stream Deck's token cannot drive an assistant, and an
//     assistant's token cannot be replayed against the REST API.
//  3. The levels: Read, Content, Control (db.MCPLevel*). A tool in a level that
//     is off is not registered on the server the request is given, so it is
//     absent from tools/list and a direct call is the SDK's unknown-tool error
//     -- the same answer a read token gets for a write tool.
//
// The effective tool set is the token's tier INTERSECTED with the enabled
// levels. An mcp-read token reaches Read only, whatever is switched on. An
// mcp-admin token reaches every enabled level. Content and Control are never
// reachable by a read-tier token, because the tier check is not replaced by
// the levels -- it is kept, and the levels are applied on top.
//
// Who may change any of this: a signed-in operator, on the session-only
// /api/v1/mcp/access route. No API token reaches it, admin tokens included, so
// a stolen token cannot switch MCP on for itself. The switch lives in its own
// table rather than in the settings document for the same reason: an admin
// token may PUT /settings.

// mcpLevelOf maps a tool's tier onto the level that gates it. One to one: the
// three tiers are the three levels, and TestTheMCPToolLevelMatrix pins which
// tool is in which.
func mcpLevelOf(t mcpTier) string {
	switch t {
	case mcpRead:
		return db.MCPLevelRead
	case mcpContent:
		return db.MCPLevelContent
	default:
		return db.MCPLevelControl
	}
}

// mcpAccess reads the switch. A store error reads as OFF: a database that
// cannot answer must not be the thing that turns an endpoint on.
func (s *Server) mcpAccess() db.MCPAccess {
	if s.store == nil {
		return db.MCPAccess{}
	}
	a, err := s.store.GetMCPAccess()
	if err != nil {
		s.log.Warn("mcp: reading the access switch failed; treating MCP as off", "err", err)
		return db.MCPAccess{}
	}
	return a
}

// mcpGate answers /mcp with the router's not-found handler while MCP is off.
// A middleware rather than a check inside the handler, and ahead of routing,
// so that it covers every method -- the route is registered for POST, and a
// GET would otherwise be the router's 405, which says the path exists -- and
// so that it runs before Origin and authentication, which would each answer
// differently from an unknown path.
//
// notFound is read through a pointer because registerRoutes sets the router's
// not-found handler after this middleware is installed.
func (s *Server) mcpGate(notFound *http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == MCPPath && !s.mcpAccess().Enabled {
				if h := *notFound; h != nil {
					h.ServeHTTP(w, r)
				} else {
					http.NotFound(w, r)
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// mcpServerKey is one combination of tier and enabled levels.
type mcpServerKey struct {
	admin  bool
	levels string
}

// mcpServers builds one MCP server per combination on first use. The SDK
// takes a server per request, and the levels are read per request, so a
// toggle applies to the next call without a restart; the cache only saves
// rebuilding the same tool list. There are at most 2 x 8 combinations.
type mcpServers struct {
	mu   sync.Mutex
	byKy map[mcpServerKey]*mcp.Server
}

func (c *mcpServers) get(s *Server, root http.Handler, admin bool, access db.MCPAccess) *mcp.Server {
	k := mcpServerKey{admin: admin, levels: strings.Join(access.Levels, ",")}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKy == nil {
		c.byKy = map[mcpServerKey]*mcp.Server{}
	}
	if srv, ok := c.byKy[k]; ok {
		return srv
	}
	srv := s.newMCPServer(root, admin, access)
	c.byKy[k] = srv
	return srv
}

// mcpToolAllowed is the whole rule: the tier the token's scope allows, and the
// level the operator switched on. Used both to build the server (so a tool
// that fails it does not exist for the caller) and again at call time.
func mcpToolAllowed(t mcpTool, admin bool, access db.MCPAccess) bool {
	if t.tier != mcpRead && !admin {
		return false
	}
	return access.Has(mcpLevelOf(t.tier))
}

// ------------------------------------------------------------ the settings

// mcpAccessView is what the settings card reads: the switch, the levels, and
// the catalogue grouped by level so the card can say what each one holds.
type mcpAccessView struct {
	Enabled   bool                `json:"enabled"`
	Levels    []string            `json:"levels"`
	AllLevels []string            `json:"allLevels"`
	Tools     map[string][]string `json:"tools"`
	Endpoint  string              `json:"endpoint"`
}

func viewMCPAccess(a db.MCPAccess) mcpAccessView {
	tools := map[string][]string{}
	for _, t := range mcpCatalogue() {
		l := mcpLevelOf(t.tier)
		tools[l] = append(tools[l], t.name)
	}
	levels := a.Levels
	if !a.Enabled {
		levels = []string{}
	}
	return mcpAccessView{Enabled: a.Enabled, Levels: levels, AllLevels: db.MCPLevels(), Tools: tools, Endpoint: MCPPath}
}

func (s *Server) handleGetMCPAccess(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, viewMCPAccess(s.mcpAccess()))
}

// handlePutMCPAccess stores the switch and the levels. The FIRST enable of an
// install that has never chosen levels turns on Read and nothing else, so an
// operator flipping the switch to try it out does not also hand an assistant
// the chat log and the stop buttons. A body that names levels is taken as
// written.
func (s *Server) handlePutMCPAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool      `json:"enabled"`
		Levels  *[]string `json:"levels"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	before, err := s.store.GetMCPAccess()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	levels := before.Levels
	if req.Levels != nil {
		levels = *req.Levels
	}
	after, err := s.store.PutMCPAccess(req.Enabled, levels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !sameMCPAccess(before, after) {
		s.log.Info("mcp access changed", "enabled", after.Enabled, "levels", strings.Join(after.Levels, ","))
		s.publishAudit(auditMCPAccessChanged(before, after, s.clientIP(r)))
	}
	writeJSON(w, http.StatusOK, viewMCPAccess(after))
}

func sameMCPAccess(a, b db.MCPAccess) bool {
	return a.Enabled == b.Enabled && strings.Join(a.Levels, ",") == strings.Join(b.Levels, ",")
}

// mcpAccessLabel is the switch as one line: "off", or "on (read, control)".
func mcpAccessLabel(a db.MCPAccess) string {
	if !a.Enabled {
		return "off"
	}
	if len(a.Levels) == 0 {
		return "on (no levels)"
	}
	return "on (" + strings.Join(a.Levels, ", ") + ")"
}

// auditMCPAccessChanged records a change of the switch or its levels, before
// and after. CRITICAL when the change leaves Control reachable where it was
// not: from then on an assistant holding an admin MCP token can stop a
// broadcast, which is the same weight as minting an admin token. Warning
// otherwise -- including switching MCP off, which narrows.
func auditMCPAccessChanged(before, after db.MCPAccess, address string) alerts.Event {
	sev := alerts.SeverityWarning
	if after.Has(db.MCPLevelControl) && !before.Has(db.MCPLevelControl) {
		sev = alerts.SeverityCritical
	}
	return alerts.Event{
		Type:     alerts.TypeSettingsChanged,
		Severity: sev,
		Title:    "MCP access changed",
		Text: "AI assistant access (MCP) was changed from " + mcpAccessLabel(before) +
			" to " + mcpAccessLabel(after) + ".",
	}.
		WithField(fieldSections, "mcp").
		WithField("Before", mcpAccessLabel(before)).
		WithField("After", mcpAccessLabel(after)).
		WithField(fieldAddress, address)
}
