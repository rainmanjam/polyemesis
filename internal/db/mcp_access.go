package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MCP capability levels: which groups of MCP tools exist for an MCP token.
// They sit under MCPAccess.Enabled and combine with the token's own tier (see
// internal/api/mcp_access.go): Content and Control are only ever reachable by
// an admin-tier MCP token, whatever is switched on here.
const (
	// MCPLevelRead is the status tools and the two resources.
	MCPLevelRead = "read"
	// MCPLevelContent is what viewers wrote and what was said on stream:
	// chat search, a chat user's card, transcript search.
	MCPLevelContent = "content"
	// MCPLevelControl is every tool that changes a broadcast.
	MCPLevelControl = "control"
)

// MCPLevels is every level, in the order the settings card lists them.
func MCPLevels() []string { return []string{MCPLevelRead, MCPLevelContent, MCPLevelControl} }

// MCPAccess is the MCP switch and its levels.
type MCPAccess struct {
	Enabled bool `json:"enabled"`
	// Levels holds the enabled levels, in MCPLevels order, never nil.
	Levels    []string  `json:"levels"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Has reports whether level is enabled. A disabled switch has no levels.
func (a MCPAccess) Has(level string) bool {
	if !a.Enabled {
		return false
	}
	for _, l := range a.Levels {
		if l == level {
			return true
		}
	}
	return false
}

// defaultMCPLevels is what the first enable turns on: Read, and nothing that
// reads viewers' words or changes a broadcast.
func defaultMCPLevels() []string { return []string{MCPLevelRead} }

// GetMCPAccess reads the switch. No row is OFF with the default levels -- the
// state of every install that has never touched it.
func (d *DB) GetMCPAccess() (MCPAccess, error) {
	var (
		enabled int
		raw     string
		updated int64
	)
	err := d.sql.QueryRow(`SELECT enabled, levels, updated_at FROM mcp_access WHERE id = 1`).Scan(&enabled, &raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPAccess{Levels: defaultMCPLevels()}, nil
	}
	if err != nil {
		return MCPAccess{}, err
	}
	var stored []string
	if json.Unmarshal([]byte(raw), &stored) != nil {
		// A row this build cannot read keeps the switch as stored but
		// narrows to the default levels: unreadable must not mean "more".
		stored = defaultMCPLevels()
	}
	a := MCPAccess{Enabled: enabled != 0, Levels: normalizeMCPLevels(stored)}
	if updated > 0 {
		a.UpdatedAt = time.Unix(updated, 0)
	}
	return a, nil
}

// PutMCPAccess stores the switch and its levels. An unknown level is refused
// rather than dropped: the operator asked for something this build cannot
// grant, and saying so beats storing a narrower set than they believe.
func (d *DB) PutMCPAccess(enabled bool, levels []string) (MCPAccess, error) {
	for _, l := range levels {
		if !validMCPLevel(l) {
			return MCPAccess{}, fmt.Errorf("unknown MCP level %q; use read, content or control", l)
		}
	}
	norm := normalizeMCPLevels(levels)
	b, err := json.Marshal(norm)
	if err != nil {
		return MCPAccess{}, err
	}
	now := time.Now().Unix()
	if _, err := d.sql.Exec(
		`INSERT INTO mcp_access (id, enabled, levels, updated_at) VALUES (1, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET enabled = excluded.enabled, levels = excluded.levels, updated_at = excluded.updated_at`,
		boolInt(enabled), string(b), now); err != nil {
		return MCPAccess{}, err
	}
	return MCPAccess{Enabled: enabled, Levels: norm, UpdatedAt: time.Unix(now, 0)}, nil
}

func validMCPLevel(l string) bool {
	for _, k := range MCPLevels() {
		if k == l {
			return true
		}
	}
	return false
}

// normalizeMCPLevels keeps the known levels, once each, in MCPLevels order.
func normalizeMCPLevels(in []string) []string {
	want := map[string]bool{}
	for _, l := range in {
		want[l] = true
	}
	out := []string{}
	for _, l := range MCPLevels() {
		if want[l] {
			out = append(out, l)
		}
	}
	return out
}
