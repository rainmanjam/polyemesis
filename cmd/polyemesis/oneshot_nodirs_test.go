package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
)

// runAsServerEnv makes this test binary behave as the polyemesis binary: TestMain
// hands its arguments to run() instead of running tests. It is how the tests
// below drive the real flag handling end to end without a build step and
// without a shell -- the same re-exec device as internal/engine's TestMain.
const runAsServerEnv = "POLYEMESIS_TEST_RUN_AS_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(runAsServerEnv) == "1" {
		if err := run(nil); err != nil {
			fmt.Fprintf(os.Stderr, "\npolyemesis: %v\n\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runServer runs this binary as polyemesis with args, in dir, and returns its
// combined output and whether it exited 0.
func runServer(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), runAsServerEnv+"=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Stdin = strings.NewReader("")
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("could not run the server process: %v", err)
		}
	}
	return out.String(), err == nil
}

// THE ONE-SHOT COMMANDS USED TO BUILD AN INSTALL WHERE THE OPERATOR WAS STANDING.
//
// -reset-admin and -verify-backup ran after EnsureDirs, and db.Open creates
// the file it is pointed at. So on a manual install -- whose unit passes
// --data /var/lib/polyemesis while the copied config still says
// dataDir: "./data" -- `polyemesis -config config.yaml -reset-admin` created
// ./data/{fonts,hls,tls,...} and an empty polyemesis.db in the current
// directory and then advised "complete first-run setup". Reproduced twice in
// the staging-readiness review (row 10).
//
// Mutation: move the two branches back below cfg.EnsureDirs() in run().
// Observed to fail with ./data created by both commands. Mutation: drop the
// os.Stat refusal in resetAdmin. Observed to fail here with the refusal no
// longer naming the path, and in the test below with polyemesis.db created and
// "first-run setup" printed.
func TestOneShotCommandsCreateNothingWhereTheyAreRun(t *testing.T) {
	cwd := t.TempDir()
	cfgPath := filepath.Join(cwd, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("addr: \"127.0.0.1:8080\"\ndataDir: \"./data\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(cwd, "data")

	out, ok := runServer(t, cwd, "-config", cfgPath, "-reset-admin")
	if ok {
		t.Fatalf("-reset-admin against a data directory with no database exited 0:\n%s", out)
	}
	if !strings.Contains(out, "no database at") || !strings.Contains(out, filepath.Join(stray, "polyemesis.db")) {
		t.Errorf("-reset-admin did not name the absolute database path it looked for:\n%s", out)
	}
	if strings.Contains(out, "first-run setup") {
		t.Errorf("-reset-admin told the owner of an existing install to run first-run setup:\n%s", out)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("-reset-admin created %s in the directory it was run from", stray)
	}

	out, ok = runServer(t, cwd, "-config", cfgPath, "-verify-backup", filepath.Join(cwd, "no-such-backup"))
	if ok {
		t.Fatalf("-verify-backup of a directory that does not exist exited 0:\n%s", out)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Errorf("-verify-backup created %s in the directory it was run from", stray)
	}
}

// resetAdmin itself, for the path a caller other than run() would take: an
// existing, empty directory, where db.Open would happily create the file.
func TestResetAdminRefusesADatabaseThatIsNotThere(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dataDir
	dbFile := filepath.Join(dataDir, "polyemesis.db")

	var out bytes.Buffer
	err := resetAdmin(cfg, strings.NewReader("a-new-password\na-new-password\n"), &out, false)
	if err == nil {
		t.Fatal("resetAdmin reset a password in a database that did not exist")
	}
	if !strings.Contains(err.Error(), "no database at "+dbFile) || !strings.Contains(err.Error(), "-data /var/lib/polyemesis") {
		t.Errorf("the refusal does not name the path and the flag that fixes it: %v", err)
	}
	if _, serr := os.Stat(dbFile); serr == nil {
		t.Errorf("resetAdmin created %s while refusing", dbFile)
	}
}
