//go:build ignore

// Package main -- resolveRelayPort, the one function all five acceptance drivers
// share that driverhelpers.go cannot carry: postprod keeps its own waitUp and
// get, so it cannot take that file, and this one is named beside either.
// Same reason it is not a package as driverhelpers.go's header gives: `go run`
// from /tmp cannot resolve a module import. It needs `get` and `die` from the
// driver it is compiled with.
package main

import (
	"strconv"
	"strings"
	"time"
)

// resolveRelayPort: the shell's lsof is a hint, not a precondition. Without a
// seeded source no relay socket exists until this driver creates one, so an
// empty value means "ask the server", not "fail". The full account of the cycle
// is in driverlib.ResolveRelayPort; this file cannot import it, because `go run`
// resolves module imports against the cwd and these suites run from /tmp.
func resolveRelayPort(fromShell string) int {
	if p, err := strconv.Atoi(strings.TrimSpace(fromShell)); err == nil && p > 0 {
		return p
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		relay, _ := get("/stats")["relay"].(map[string]any)
		if pf, ok := relay["port"].(float64); ok && pf > 0 {
			return int(pf)
		}
	}
	die("no relay port after 30s; the source was probably never created")
	return 0
}
