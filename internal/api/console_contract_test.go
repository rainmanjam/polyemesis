package api

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// THE CONSOLE'S ROUTES, HELD TO THE ROUTER.
//
// ui/src/lib/api.ts (and the generic verbs the console reaches through
// lib/autoApi.ts) write every path by hand. Nothing compiled them against the
// router, so a route renamed or removed on the server would leave a button that
// 404s while both suites stayed green. This reads the UI source the way
// streamdeck_contract_test.go reads the Stream Deck client and checks that every
// call names a route the real router serves for that method.

const uiSrcDir = "../../ui/src"

// consoleCall is one HTTP call site in the console's source.
type consoleCall struct {
	Method string
	// Path is relative to /api/v1, query string dropped, each ${expr} written {}.
	Path string
	File string
	Line int
}

var consoleVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "del": "DELETE", "delWithBody": "DELETE",
}

var consoleCallee = regexp.MustCompile(`(?:^|[^\w.$])((?:autoApi\.)?)(get|post|put|patch|del|delWithBody)\b`)

// parseConsoleCalls finds the verb helper calls in one file: a callee from
// consoleVerbs, an optional <type> argument, then a string or template literal
// path. prefix is "autoApi." for files other than api.ts.
func parseConsoleCalls(file, src, prefix string) ([]consoleCall, error) {
	lineOf := func(i int) int { return strings.Count(src[:i], "\n") + 1 }
	var out []consoleCall
	for _, m := range consoleCallee.FindAllStringSubmatchIndex(src, -1) {
		if src[m[2]:m[3]] != prefix {
			continue
		}
		verb := src[m[4]:m[5]]
		i := m[5]
		skip := func() {
			for i < len(src) && strings.ContainsRune(" \t\r\n", rune(src[i])) {
				i++
			}
		}
		skip()
		if i < len(src) && src[i] == '<' {
			depth := 0
			for ; i < len(src); i++ {
				switch {
				case src[i] == '=' && i+1 < len(src) && src[i+1] == '>':
					i++ // an arrow inside a type, not a closing >
				case src[i] == '<':
					depth++
				case src[i] == '>':
					if depth--; depth == 0 {
						goto closed
					}
				}
			}
		closed:
			i++
			skip()
		}
		if i >= len(src) || src[i] != '(' {
			continue // a definition, a destructuring, a property: not a call
		}
		i++
		skip()
		if i >= len(src) || (src[i] != '"' && src[i] != '`') {
			return nil, fmt.Errorf("%s:%d: %s( is not followed by a string or template path", file, lineOf(i), verb)
		}
		quote := src[i]
		var path strings.Builder
		for i++; i < len(src) && src[i] != quote; i++ {
			if src[i] == '$' && quote == '`' && i+1 < len(src) && src[i+1] == '{' {
				depth := 0
				for ; i < len(src); i++ {
					if src[i] == '{' {
						depth++
					} else if src[i] == '}' {
						if depth--; depth == 0 {
							break
						}
					}
				}
				path.WriteString("{}")
				continue
			}
			path.WriteByte(src[i])
		}
		p := path.String()
		// A trailing ${...} glued to a segment is a query string built elsewhere.
		if strings.HasSuffix(p, "{}") && !strings.HasSuffix(p, "/{}") {
			p = strings.TrimSuffix(p, "{}")
		}
		if q := strings.IndexByte(p, '?'); q >= 0 {
			p = p[:q]
		}
		out = append(out, consoleCall{Method: consoleVerbs[verb], Path: p, File: file, Line: lineOf(m[0])})
	}
	return out, nil
}

func loadConsoleCalls(t *testing.T) []consoleCall {
	t.Helper()
	var out []consoleCall
	err := filepath.Walk(uiSrcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) ||
			strings.Contains(p, ".test.") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		prefix := "autoApi."
		if strings.HasSuffix(p, filepath.Join("lib", "api.ts")) {
			prefix = ""
		}
		calls, err := parseConsoleCalls(strings.TrimPrefix(p, uiSrcDir+"/"), string(raw), prefix)
		out = append(out, calls...)
		return err
	})
	if err != nil {
		t.Fatalf("read the console's source: %v", err)
	}
	return out
}

func TestTheConsoleCallParserReadsWhatItShould(t *testing.T) {
	src := "const get = <T,>(p: string) => request<T>(p);\n" +
		"x: () => get<Array<{ a: () => void }>>(\"/a\"),\n" +
		"y: (id: number) => post<void>(`/b/${id}/c?force=1`),\n" +
		"q: (qs: string) => get<void>(`/e${qs}`),\n" +
		"z: () => map.get(\"/nope\"),\n" +
		"w: (n: string) => delWithBody<void>(`/d/${encodeURIComponent(n)}`, {}),\n"
	got, err := parseConsoleCalls("f.ts", src, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []consoleCall{
		{"GET", "/a", "f.ts", 2}, {"POST", "/b/{}/c", "f.ts", 3}, {"GET", "/e", "f.ts", 4}, {"DELETE", "/d/{}", "f.ts", 6},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parsed\n  %+v\nwant\n  %+v", got, want)
	}
	if _, err := parseConsoleCalls("f.ts", "get(path)", ""); err == nil {
		t.Error("a call whose path is not a literal was skipped, not refused")
	}
}

// TestTheConsoleCallsOnlyRoutesTheServerServes checks every literal path the
// console calls against the router, by method.
func TestTheConsoleCallsOnlyRoutesTheServerServes(t *testing.T) {
	calls := loadConsoleCalls(t)
	if len(calls) < 150 {
		t.Fatalf("found %d console calls; api.ts alone makes about 190, so this would check almost nothing", len(calls))
	}
	h, _, _ := sourceServer(t)
	mux, ok := h.(*chi.Mux)
	if !ok {
		t.Fatalf("Server.Handler returned %T, not *chi.Mux; this check looks routes up in the real router", h)
	}
	var missing []string
	for _, c := range calls {
		concrete := "/api/v1" + strings.ReplaceAll(c.Path, "{}", "1")
		if mux.Find(chi.NewRouteContext(), c.Method, concrete) == "" {
			missing = append(missing, fmt.Sprintf("ui/src/%s:%d  %s %s", c.File, c.Line, c.Method, c.Path))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the console calls %d route(s) the router does not serve:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
