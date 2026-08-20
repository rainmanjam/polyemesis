package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ISSUE #440. THE GUARD FOR A MEMORY-SAFETY RULE NO TOOL CHECKS.
//
// job_windows.go converts a pointer to a uintptr and hands the integer to the
// kernel through windows.SetInformationJobObject. unsafe's pattern (4) permits
// that only when the compiler arranges for the referenced object to be
// "retained AND NOT MOVED until the call completes", and it only does that for
// a call to a function implemented in assembly, or one marked
// //go:uintptrescapes, or a nosplit chain. windows.SetInformationJobObject is
// none of those: it is an ordinary, splittable Go function that happens to take
// a uintptr.
//
// What that costs, if the directive is ever dropped: `info` goes back to being
// a stack local, the wrapper's own prologue can call morestack, copystack
// relocates the frame, and the integer we already computed keeps pointing at
// the OLD stack -- which is freed and reused by then. The kernel writes a
// 112-byte struct into it. The crash lands later, in whatever goroutine
// inherited the memory, as "fatal error: found pointer to free object", and
// names a package with nothing to do with job objects. That is the shape of the
// report in #440.
//
// WHY THIS TEST LOOKS LIKE THIS. Nothing in the toolchain flags the unfixed
// form:
//
//   - `go vet ./...` runs on Linux, where job_windows.go is not built at all.
//   - `GOOS=windows go vet ./...` is clean too. vet's unsafeptr check looks for
//     uintptr -> Pointer; this is Pointer -> uintptr, the opposite direction.
//   - -d=checkptr and -race only fire on a machine that executes the bad
//     conversion, i.e. Windows, and only if the stack happens to move inside a
//     window a few instructions wide. #440 reproduced roughly once in six runs.
//
// So the assertion is made against the one component that does know the answer:
// the escape analysis. The compiler is asked, in -m mode, whether each struct
// whose address becomes a uintptr was forced onto the heap -- heap objects are
// never relocated by Go's collector, so a heap address cannot go stale. This
// runs on every platform, including the Linux and macOS jobs where the Windows
// files are otherwise dead text, which is the whole point: the regression it
// guards is invisible everywhere the tests actually run.
//
// It is a compile, not a run. Nothing here executes Windows code.
func TestPointersHandedToTheKernelAsUintptrsAreForcedOntoTheHeap(t *testing.T) {
	diagnostics := windowsEscapeAnalysis(t)

	// Every site in this package where a Go pointer becomes a uintptr that the
	// kernel will dereference. Adding a syscall in this shape means adding a row.
	sites := []struct {
		file     string
		variable string
		why      string
	}{
		{
			file:     "job_windows.go",
			variable: "info",
			why: "ensureJob's JOBOBJECT_EXTENDED_LIMIT_INFORMATION: the kernel READS it after " +
				"the wrapper has had a chance to grow the stack, so a stack copy would leave the " +
				"kernel reading freed memory and the job silently unlimited",
		},
		{
			file:     "job_windows_test.go",
			variable: "want",
			why:      "the round-trip test's request struct, same read hazard as ensureJob's",
		},
		{
			file:     "job_windows_test.go",
			variable: "got",
			why: "the round-trip test's result struct, and this one is worse: the kernel WRITES " +
				"through the integer, so a stack copy has it scribble 112 bytes into freed stack " +
				"memory while the test reads its own uninitialised variable and passes",
		},
	}

	for _, site := range sites {
		t.Run(site.file+":"+site.variable, func(t *testing.T) {
			if !reportsMovedToHeap(diagnostics, site.file, site.variable) {
				t.Fatalf("the compiler did not move %s in %s to the heap, so its address is a "+
					"stack address that copystack can invalidate while the syscall is in flight.\n"+
					"Why it matters here: %s.\n"+
					"The fix is not runtime.KeepAlive -- KeepAlive is special-cased NOT to force "+
					"its argument to escape, which is how #440 survived its first fix. The "+
					"conversion has to sit in the argument list of a //go:uintptrescapes wrapper; "+
					"see setInformationJobObject in job_windows.go.\n"+
					"If the directive IS present and this still fails, check whether the compiler "+
					"renamed the %q diagnostic before assuming the code is wrong.",
					site.variable, site.file, site.why, "moved to heap")
			}
		})
	}

	// The other half of the same fact, read from the wrapper's side rather than
	// the caller's: the compiler only prints this when it has honoured a
	// //go:uintptrescapes directive on a uintptr parameter. Without it the
	// "moved to heap" lines above could in principle be produced by some
	// unrelated escape, and the guard would pass for the wrong reason.
	for _, file := range []string{"job_windows.go", "job_windows_test.go"} {
		if !hasDiagnostic(diagnostics, file, "marking info as escaping uintptr") {
			t.Errorf("%s has no //go:uintptrescapes wrapper in effect: the compiler never "+
				"reported marking a uintptr parameter as escaping. The heap allocation asserted "+
				"above is then an accident of some other escape rather than a guarantee.", file)
		}
	}
}

// windowsEscapeAnalysis cross-compiles this package's test binary for Windows
// with -gcflags=-m and returns the compiler's escape diagnostics.
//
// `go test -c` rather than `go build`, because two of the three conversions
// live in job_windows_test.go and `go build` never compiles test files. `-o`
// discards the binary: the diagnostics are the product here.
//
// No skip if the toolchain is missing. A test that quietly does nothing is how
// a rule like this rots; `go test` implies a toolchain, so its absence is a
// broken environment and should read as a failure.
func windowsEscapeAnalysis(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "test", "-c", "-o", os.DevNull, "-gcflags=-m", "./internal/supervisor")
	cmd.Dir = moduleRoot(t)
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cross-compiling internal/supervisor for windows/amd64 failed: %v\n%s", err, out)
	}
	return string(out)
}

// reportsMovedToHeap reports whether the compiler said it moved variable to the
// heap in file. The diagnostics are matched line by line so that a name which
// merely appears inside some other message cannot satisfy the assertion.
func reportsMovedToHeap(diagnostics, file, variable string) bool {
	return hasDiagnostic(diagnostics, file, "moved to heap: "+variable)
}

func hasDiagnostic(diagnostics, file, message string) bool {
	for _, line := range strings.Split(diagnostics, "\n") {
		// internal/supervisor/job_windows.go:64:3: moved to heap: info
		//
		// The file name is matched as a substring because the compiler prints
		// the path in the host's separator style. It cannot collide: the only
		// other candidate here is job_windows_test.go, which does not contain
		// "job_windows.go".
		line = strings.TrimSpace(line)
		if strings.Contains(line, file) && strings.HasSuffix(line, ": "+message) {
			return true
		}
	}
	return false
}

// moduleRoot walks up from the test's working directory to the module root, so
// this test does not encode how deep internal/supervisor sits.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
