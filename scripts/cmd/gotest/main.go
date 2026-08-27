// Command gotest runs `go test` and refuses to let a Go RUNTIME abort be read
// as an ordinary test failure.
//
// WHY THIS EXISTS. #440 is a heap corruption that has fired three times in many
// hundreds of Windows CI runs, with three different runtime messages, and has
// never reproduced. Each time, the only thing distinguishing it from a flaky
// test was somebody reading the log carefully enough to notice that the line
// said `fatal error:` rather than `--- FAIL:`. Two of the three were nearly
// waved through; the issue's own comments say as much, and say that the sample
// size is the instrument.
//
// An instrument nobody reads is not an instrument. So the distinction is made
// by a program: a runtime abort is annotated as one, named as #440, and printed
// with the exact message the runtime chose, which is the only part that varies
// between occurrences and the only part worth collecting.
//
// It changes no verdict. A run that fails still fails and a run that passes
// still passes; what changes is whether the NEXT person can tell which kind of
// failure they are looking at without knowing this bug exists.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Go prints `fatal error:` for a runtime throw -- heap corruption, a bad
// pointer, a deadlock -- and `panic:` for ordinary program panics, which
// include every failing test that panics. Only the first is interesting here.
const abortPrefix = "fatal error:"

// NOT EVERY `fatal error:` IS A CORRUPTION, and this list is the difference
// between an instrument and a nuisance. Go's own test timeout is reported
// through the same channel, and so is a deadlock, and both are ordinary
// failures with ordinary explanations. Flagging them as #440 would train the
// next reader to ignore the annotation, which is precisely the outcome this
// exists to prevent.
var benign = []string{
	"test timed out",
	"all goroutines are asleep",
	"stack overflow",
	"concurrent map",
}

func isRuntimeAbort(line string) bool {
	i := strings.Index(line, abortPrefix)
	if i < 0 {
		return false
	}
	rest := strings.ToLower(line[i+len(abortPrefix):])
	for _, b := range benign {
		if strings.Contains(rest, b) {
			return false
		}
	}
	return true
}

func main() {
	// Everything after the program name is handed to `go test` untouched, so
	// the workflow keeps saying what it always said and this wraps rather than
	// reinterprets it.
	cmd := exec.Command("go", append([]string{"test"}, os.Args[1:]...)...)
	cmd.Stdin = os.Stdin

	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gotest: could not read go test's output:", err)
		os.Exit(2)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "gotest: could not start go test:", err)
		os.Exit(2)
	}

	var aborts []string
	sc := bufio.NewScanner(out)
	// A goroutine dump line can be long; the default 64KiB token is not
	// generous enough to be sure, and a scanner that stops early would drop
	// the output this wraps.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		// STREAMED, NOT COLLECTED. The output has to keep arriving as it is
		// produced: a CI job whose log appears only at the end is one nobody
		// can watch, and a job killed by a timeout would print nothing at all.
		fmt.Println(line)
		if isRuntimeAbort(line) {
			aborts = append(aborts, strings.TrimSpace(line))
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		fmt.Fprintln(os.Stderr, "gotest: stopped reading go test's output:", err)
	}

	code := 0
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			fmt.Fprintln(os.Stderr, "gotest: go test did not finish:", err)
			code = 2
		}
	}

	if len(aborts) > 0 {
		report(aborts)
		// Non-zero even if `go test` somehow returned 0. A runtime abort that
		// left the exit status clean is a worse version of the same problem.
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func asExitError(err error, dst **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*dst = ee
	}
	return ok
}

// report writes the annotation. GitHub renders `::error` at the top of the run
// and in the pull request's Files view, which is where somebody skimming a red
// check actually looks -- not three thousand lines into a log.
func report(aborts []string) {
	fmt.Println()
	fmt.Println("================================================================")
	fmt.Println("THIS IS NOT A TEST FAILURE. The Go runtime aborted.")
	fmt.Println()
	for _, a := range aborts {
		fmt.Println("   ", a)
	}
	fmt.Println()
	fmt.Println("A `fatal error:` from the runtime means the runtime itself found")
	fmt.Println("something impossible -- most often a heap it can no longer trust.")
	fmt.Println("In a program with no cgo it should not be reachable at all.")
	fmt.Println()
	fmt.Println("This is issue #440. It is intermittent -- three occurrences in many")
	fmt.Println("hundreds of runs, three different messages, never reproduced -- so")
	fmt.Println("the SAMPLE SIZE is the instrument. Please record this occurrence on")
	fmt.Println("the issue with the message above, the commit, and the run URL, even")
	fmt.Println("if a re-run goes green. A re-run going green is expected and is not")
	fmt.Println("evidence of anything.")
	fmt.Println()
	fmt.Println("Do NOT re-run and move on without recording it. That is how the")
	fmt.Println("first two occurrences were nearly lost.")
	fmt.Println("================================================================")

	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::error title=Go runtime abort (issue #440)::%s -- NOT a test failure. "+
			"Record this on issue #440 with the commit and run URL before re-running.\n",
			strings.Join(aborts, " | "))
	}
}
