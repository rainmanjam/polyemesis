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
// ui/src writes every API path by hand. Nothing compiled them against the
// router, so a route renamed or removed on the server would leave a button that
// 404s while both suites stayed green. This reads the UI source the way
// streamdeck_contract_test.go reads the Stream Deck client and checks that every
// call names a route the real router serves for that method.
//
// EVERY CALL FORM is read: the verb helpers (get/post/put/patch/del/delWithBody,
// autoApi.*), request and requestRaw, fetch and XMLHttpRequest. A call whose
// path is not a literal is an error, never a skip, unless it is in
// consoleDynamicCalls with the reason it cannot be checked (or the route it
// really reaches, which is then checked).

const uiSrcDir = "../../ui/src"

// consoleCall is one HTTP call site in the console's source.
type consoleCall struct {
	Method string
	// Path is the full request path, query string dropped, each ${expr} written {}.
	Path string
	File string
	Line int
}

// consoleDynamic is a call whose path is not a literal at the call site.
type consoleDynamic struct {
	Reason string
	// Method and Path, when the call really reaches one fixed route (full path).
	Method, Path string
}

// consoleDynamicCalls is keyed by "file|first argument as written".
var consoleDynamicCalls = map[string]consoleDynamic{
	"lib/api.ts|p":                                 {Reason: "the verb helpers (get/post/put/patch/del) forwarding their own path to request; every caller is a literal checked above"},
	"lib/api.ts|path":                              {Reason: "request forwarding its path to requestRaw"},
	"lib/api.ts|BASE + path":                       {Reason: "requestRaw itself: the one place a request is built"},
	"lib/api.ts|url":                               {Reason: "playoutPublic builds BASE + \"/playout/public\" + tokenQuery(token)", Method: "GET", Path: "/api/v1/playout/public"},
	"features/player/liveCaptions.ts|url":          {Reason: "the captions URL PublicPlayer derives from the master playlist's own directory; not a fixed route"},
	"pages/ClipEditor.tsx|seg.spriteVtt as string": {Reason: "a recording's sprite sheet path, served from the media tree, not an API route"},
}

var consoleVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "del": "DELETE", "delWithBody": "DELETE",
}

var consoleCallee = regexp.MustCompile(`(?:^|[^\w.$])((?:autoApi\.|xhr\.)?)(get|post|put|patch|del|delWithBody|requestRaw|request|fetch|open)\b`)

var consoleMethodOpt = regexp.MustCompile(`method:\s*"(GET|POST|PUT|PATCH|DELETE)"`)

// blankComments replaces comments with spaces (newlines kept, so line numbers
// hold) without touching string and template literals.
func blankComments(src string) string {
	b := []byte(src)
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == '"' || c == '\'' || c == '`':
			for i++; i < len(b) && b[i] != c; i++ {
				if b[i] == '\\' {
					i++
				}
			}
			i++
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for ; i < len(b) && b[i] != '\n'; i++ {
				b[i] = ' '
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			for end := i + 2; ; end++ {
				if end+1 >= len(b) || (b[end] == '*' && b[end+1] == '/') {
					stop := min(end+2, len(b))
					for ; i < stop; i++ {
						if b[i] != '\n' {
							b[i] = ' '
						}
					}
					break
				}
			}
		default:
			i++
		}
	}
	return string(b)
}

// splitArgs reads a call's arguments from just after its "(", returning the
// top-level arguments as written and the index after the closing ")".
func splitArgs(src string, i int) ([]string, int) {
	var args []string
	depth, start := 0, i
	for ; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			for i++; i < len(src) && src[i] != c; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				if t := strings.TrimSpace(src[start:i]); t != "" {
					args = append(args, t)
				}
				return args, i + 1
			}
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(src[start:i]))
				start = i + 1
			}
		}
	}
	return args, i
}

// literalPath reads a path expression: an optional `BASE +`, then a string or
// template literal (anything after it, such as `+ sourceQuery(x)`, is a query).
func literalPath(expr string) (path string, base, ok bool) {
	e := strings.TrimSpace(expr)
	if rest, found := strings.CutPrefix(e, "BASE"); found {
		if rest = strings.TrimSpace(rest); strings.HasPrefix(rest, "+") {
			e, base = strings.TrimSpace(rest[1:]), true
		}
	}
	if e == "" || (e[0] != '"' && e[0] != '`') {
		return "", base, false
	}
	quote := e[0]
	var p strings.Builder
	for i := 1; i < len(e) && e[i] != quote; i++ {
		if e[i] == '$' && quote == '`' && i+1 < len(e) && e[i+1] == '{' {
			depth := 0
			for ; i < len(e); i++ {
				if e[i] == '{' {
					depth++
				} else if e[i] == '}' {
					if depth--; depth == 0 {
						break
					}
				}
			}
			p.WriteString("{}")
			continue
		}
		p.WriteByte(e[i])
	}
	s := p.String()
	// A trailing ${...} glued to a segment is a query string built elsewhere.
	if strings.HasSuffix(s, "{}") && !strings.HasSuffix(s, "/{}") {
		s = strings.TrimSuffix(s, "{}")
	}
	if q := strings.IndexByte(s, '?'); q >= 0 {
		s = s[:q]
	}
	return s, base, true
}

// parseConsoleCalls finds every HTTP call in one file. api is true for
// lib/api.ts, whose bare get/post/... are the verb helpers; elsewhere only the
// autoApi.* forms are. dynamic lists the calls whose path is not a literal.
func parseConsoleCalls(file, src string, api bool) (calls []consoleCall, dynamic []string, err error) {
	src = blankComments(src)
	lineOf := func(i int) int { return strings.Count(src[:i], "\n") + 1 }
	for _, m := range consoleCallee.FindAllStringSubmatchIndex(src, -1) {
		prefix, name := src[m[2]:m[3]], src[m[4]:m[5]]
		_, isVerb := consoleVerbs[name]
		switch {
		case prefix == "xhr." && name != "open", prefix == "" && name == "open":
			continue
		case prefix == "xhr.":
		case prefix == "autoApi." && !isVerb:
			continue
		case prefix == "" && isVerb && !api:
			continue
		case prefix == "" && isVerb:
		case prefix == "" && (name == "request" || name == "requestRaw" || name == "fetch"):
			if strings.HasSuffix(strings.TrimSpace(src[:m[4]]), "function") {
				continue // the definition
			}
		default:
			continue
		}
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
					i++
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
		args, _ := splitArgs(src, i+1)
		if len(args) == 0 {
			continue
		}
		method, pathArg, opts := consoleVerbs[name], args[0], ""
		if prefix == "xhr." {
			if len(args) < 2 || len(args[0]) < 3 || args[0][0] != '"' {
				return nil, nil, fmt.Errorf("%s:%d: xhr.open without a literal method and a path", file, lineOf(m[0]))
			}
			method, pathArg = strings.Trim(args[0], `"`), args[1]
		} else if !isVerb {
			method = "GET"
			if len(args) > 1 {
				opts = args[1]
			}
			if mm := consoleMethodOpt.FindStringSubmatch(opts); mm != nil {
				method = mm[1]
			}
		}
		path, base, ok := literalPath(pathArg)
		if !ok {
			dynamic = append(dynamic, file+"|"+pathArg)
			continue
		}
		// request/requestRaw/verbs/autoApi go to /api/v1; fetch and xhr name the full path.
		if prefix == "autoApi." || isVerb || name == "request" || name == "requestRaw" || base {
			path = "/api/v1" + path
		}
		calls = append(calls, consoleCall{Method: method, Path: path, File: file, Line: lineOf(m[0])})
	}
	return calls, dynamic, nil
}

func loadConsoleCalls(t *testing.T) []consoleCall {
	t.Helper()
	var out []consoleCall
	var dynamic []string
	err := filepath.Walk(uiSrcDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !(strings.HasSuffix(p, ".ts") || strings.HasSuffix(p, ".tsx")) ||
			strings.Contains(p, ".test.") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, uiSrcDir+"/")
		calls, dyn, err := parseConsoleCalls(rel, string(raw), rel == "lib/api.ts")
		out = append(out, calls...)
		dynamic = append(dynamic, dyn...)
		return err
	})
	if err != nil {
		t.Fatalf("read the console's source: %v", err)
	}
	seen := map[string]bool{}
	for _, d := range dynamic {
		seen[d] = true
		entry, ok := consoleDynamicCalls[d]
		if !ok {
			t.Errorf("a call in ui/src whose path is not a literal: %q. Make it a literal, or list it in "+
				"consoleDynamicCalls with the reason it cannot be checked", d)
			continue
		}
		if entry.Path != "" {
			f, _, _ := strings.Cut(d, "|")
			out = append(out, consoleCall{Method: entry.Method, Path: entry.Path, File: f})
		}
	}
	for k := range consoleDynamicCalls {
		if !seen[k] {
			t.Errorf("consoleDynamicCalls lists %q, which no call in ui/src matches any more; remove it", k)
		}
	}
	return out
}

func TestTheConsoleCallParserReadsWhatItShould(t *testing.T) {
	src := "const get = <T,>(p: string) => request<T>(p);\n" +
		"x: () => get<Array<{ a: () => void }>>(\"/a\"),\n" +
		"y: (id: number) => post<void>(`/b/${id}/c?force=1`),\n" +
		"q: (qs: string) => get<void>(`/e${qs}`),\n" +
		"z: () => map.get(\"/nope\"),\n" +
		"w: (n: string) => delWithBody<void>(`/d/${encodeURIComponent(n)}`, {}),\n" +
		"// get(\"/in-a-comment\")\n" +
		"r: await requestRaw(\"/raw\", { method: \"POST\" }),\n" +
		"s: request<T>(\"/s\" + sourceQuery(1), {\n  method: \"PUT\",\n}),\n" +
		"f: await fetch(BASE + \"/f\", { method: \"POST\" }),\n" +
		"g: await fetch(\"/g\", { credentials: \"same-origin\" }),\n" +
		"h: xhr.open(\"POST\", BASE + \"/h\"),\n" +
		"export async function request<T>(path: string) {}\n"
	got, dyn, err := parseConsoleCalls("f.ts", src, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []consoleCall{
		{"GET", "/api/v1/a", "f.ts", 2}, {"POST", "/api/v1/b/{}/c", "f.ts", 3}, {"GET", "/api/v1/e", "f.ts", 4},
		{"DELETE", "/api/v1/d/{}", "f.ts", 6}, {"POST", "/api/v1/raw", "f.ts", 8}, {"PUT", "/api/v1/s", "f.ts", 9},
		{"POST", "/api/v1/f", "f.ts", 12}, {"GET", "/g", "f.ts", 13}, {"POST", "/api/v1/h", "f.ts", 14},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parsed\n  %+v\nwant\n  %+v", got, want)
	}
	if fmt.Sprint(dyn) != "[f.ts|p]" {
		t.Errorf("dynamic calls %v, want [f.ts|p]", dyn)
	}
	if _, dyn, _ := parseConsoleCalls("f.ts", "await fetch(url, {})", false); len(dyn) != 1 {
		t.Error("a fetch whose path is not a literal was skipped, not reported")
	}
}

// TestTheConsoleCallsOnlyRoutesTheServerServes checks every path the console
// calls against the router, by method.
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
		concrete := strings.ReplaceAll(c.Path, "{}", "1")
		if mux.Find(chi.NewRouteContext(), c.Method, concrete) == "" {
			missing = append(missing, fmt.Sprintf("ui/src/%s:%d  %s %s", c.File, c.Line, c.Method, c.Path))
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the console calls %d route(s) the router does not serve:\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}
