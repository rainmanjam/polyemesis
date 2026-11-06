package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// These drive each tool's run function directly, which mcp_test.go
// deliberately does not: they cover the two ways a call ends before any REST
// answer exists, so every tool's early returns are held, not only the ones a
// full client-over-HTTP test happens to reach.

// mcpRunArgs builds a schema-valid argument object for tool from its props:
// the minimum (or 1) for a number, the first enum value or a platform name
// for a string, and confirm for every boolean. Arrays are left out.
func mcpRunArgs(tool mcpTool, confirm bool) json.RawMessage {
	args := map[string]any{}
	for name, p := range tool.props {
		schema, _ := p.(map[string]any)
		switch schema["type"] {
		case "integer", "number":
			n := 1
			if m, ok := schema["minimum"].(int); ok && m > 0 {
				n = m
			}
			args[name] = n
		case "boolean":
			args[name] = confirm
		case "string":
			args[name] = mcpRunString(name, schema)
		}
	}
	b, _ := json.Marshal(args)
	return b
}

func mcpRunString(name string, schema map[string]any) string {
	if enum, ok := schema["enum"].([]string); ok && len(enum) > 0 {
		return enum[0]
	}
	switch name {
	case "platform":
		return "twitch"
	case "preset":
		return "stereo"
	case "runAt":
		return "2030-01-02T03:04:05Z"
	case "tz":
		return "UTC"
	}
	return "x"
}

// An argument the tool does not declare is refused before anything is
// dispatched, by every tool.
func TestMCPToolsRefuseAnUnknownArgument(t *testing.T) {
	for _, tool := range mcpCatalogue() {
		c := &mcpCtx{ctx: context.Background(), s: &Server{}, tool: &tool}
		res, err := tool.run(c, json.RawMessage(`{"noSuchArgument":1}`))
		var argErr *mcpArgError
		if !errors.As(err, &argErr) {
			t.Errorf("%s: err = %v (result %v), want an argument error", tool.name, err, res)
		}
		if c.wrote {
			t.Errorf("%s: a refused argument was counted as a write", tool.name)
		}
	}
}

// With the REST side unreachable (no MCP request context, so every dispatch
// fails), a confirmed call of every tool ends in an error: none reports
// success without an answer from the route it acts through.
func TestMCPToolsFailWhenTheRouteCannotBeReached(t *testing.T) {
	for _, tool := range mcpCatalogue() {
		for _, confirm := range []bool{true, false} {
			c := &mcpCtx{ctx: context.Background(), s: &Server{}, tool: &tool}
			res, err := tool.run(c, mcpRunArgs(tool, confirm))
			if confirm && err == nil {
				t.Errorf("%s (confirm): succeeded with no route to dispatch to: %v", tool.name, res)
			}
			if err == nil && res == nil {
				t.Errorf("%s (confirm=%v): neither a result nor an error", tool.name, confirm)
			}
		}
	}
}

func TestOnSource(t *testing.T) {
	one, two := int64(1), int64(2)
	sum := map[string]any{"sourceId": json.Number("1")}
	for _, tt := range []struct {
		name   string
		sum    map[string]any
		source *int64
		want   bool
	}{
		{"no filter", sum, nil, true},
		{"same source", sum, &one, true},
		{"other source", sum, &two, false},
		{"no source id", map[string]any{}, &one, false},
	} {
		if got := onSource(tt.sum, tt.source); got != tt.want {
			t.Errorf("%s: onSource = %v, want %v", tt.name, got, tt.want)
		}
	}
}
