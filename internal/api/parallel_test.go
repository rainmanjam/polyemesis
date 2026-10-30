package api

// THE PARALLEL SET.
//
// internal/api is the largest suite in the tree, and until Wave 1 every test
// in it ran sequentially. Wave 1's MCP and overlay tests pushed the package
// past the 20-minute -race deadline in CI (it panicked at 1200s with nothing
// stuck), so the slowest tests that build their own server and touch no
// package state now call t.Parallel(): the five MCP tool sweeps, the two pull
// URL spelling sweeps, the encoder list, the library re-check and the refused
// upload spelling. With the slow-reader events test, which has since left the
// set, they took about 220s in sequence under -race locally and about 100s in
// parallel.
//
// A test that asserts a WALL-CLOCK bound does not belong here. The slow-reader
// events test asserts that a stuck client's socket is closed within 20s; run
// beside the rest of this set on a macOS runner it missed that bound, because
// the bound measured the runner's load rather than the deadline.
//
// What made that safe, and what a new member of the set must keep true:
//
//   - serverUnderTest used to return "the last server built", which is only
//     the caller's when nothing runs concurrently. Every constructor now
//     records its server against the handler it returned (setTestServer), and
//     serverUnderTest looks the handler up first.
//   - No t.Setenv, no os.Chdir, no write to a package-level variable: those
//     are process-wide, and the race detector reports the last one only if two
//     tests happen to collide on the run that is watching.
//   - A sequential test is never affected. Go does not resume a parallel test
//     until every sequential top-level test in the package has finished, so
//     the tests that still read lastTestServer directly read their own.
//
// The CI deadline was raised as well (ci.yml, -timeout 25m); this set is what
// keeps the package from needing it all.
