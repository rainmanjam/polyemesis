package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stagedOver installs a "v2" over a live "v1" in a fresh directory, as a
// binary that opens database schema understood, and returns the live path.
func stagedOver(t *testing.T, understood int) string {
	t.Helper()
	dir := tempDir(t)
	bin := filepath.Join(dir, "polyemesis")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(tempDir(t), "staged")
	if err := os.WriteFile(staged, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stage(bin, staged, hashOf(t, staged), Schema{Live: understood, Understood: understood}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	return bin
}

// The rollback point is recorded with the schema the binary that became it
// opens, and the plan offers it only while the database is no newer.
func TestTheRollbackPointIsOfferedOnlyWhileItsBinaryOpensTheDatabase(t *testing.T) {
	bin := stagedOver(t, 3)
	if b, err := os.ReadFile(PreviousSchemaPath(bin)); err != nil || strings.TrimSpace(string(b)) != "3" {
		t.Fatalf("schema record = %q (%v), want 3", b, err)
	}
	for _, tc := range []struct {
		live      int
		available bool
	}{
		{2, true}, {3, true}, {4, false},
	} {
		p := PlanFor(MethodSystemd, bin, Versions{Running: "v0.5.0", LiveSchema: tc.live})
		if p.RollbackAvailable != tc.available {
			t.Errorf("live schema %d: RollbackAvailable = %v, want %v (blocked: %q)", tc.live, p.RollbackAvailable, tc.available, p.RollbackBlocked)
		}
		if !tc.available && !strings.Contains(p.RollbackBlocked, "backup") {
			t.Errorf("live schema %d: RollbackBlocked = %q, want it to point at the backup", tc.live, p.RollbackBlocked)
		}
	}
}

// A rollback to a binary that would refuse the database is refused BEFORE
// anything moves: the live binary and the rollback point are both untouched.
func TestRollbackRefusesABinaryThatWouldRefuseTheDatabase(t *testing.T) {
	bin := stagedOver(t, 1)
	err := Rollback(bin, Schema{Live: 2, Understood: 2})
	if !errors.Is(err, ErrRollbackRefused) {
		t.Fatalf("Rollback = %v, want ErrRollbackRefused", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "v2" {
		t.Errorf("a refused rollback changed the live binary to %q", b)
	}
	if b, _ := os.ReadFile(PreviousPath(bin)); string(b) != "v1" {
		t.Errorf("a refused rollback changed the rollback point to %q", b)
	}
}

// A rollback that goes ahead files the binary it replaces as the new rollback
// point, with THAT binary's schema -- the running one's -- so rolling the
// rollback back is judged on the right number.
func TestRollbackRecordsTheSchemaOfTheBinaryItSetsAside(t *testing.T) {
	bin := stagedOver(t, 1)
	if err := Rollback(bin, Schema{Live: 1, Understood: 5}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if b, _ := os.ReadFile(PreviousSchemaPath(bin)); strings.TrimSpace(string(b)) != "5" {
		t.Errorf("schema record after rollback = %q, want 5", b)
	}
}

// A rollback point left by a release before the record existed has none. Every
// such release opened schema 1 at most, so that is what it is taken to open.
func TestARollbackPointWithNoRecordIsTakenToOpenSchemaOne(t *testing.T) {
	bin := stagedOver(t, 1)
	if err := os.Remove(PreviousSchemaPath(bin)); err != nil {
		t.Fatal(err)
	}
	if why := RollbackRefusal(bin, 1); why != "" {
		t.Errorf("RollbackRefusal(live 1) = %q, want none", why)
	}
	if why := RollbackRefusal(bin, 2); why == "" {
		t.Error("RollbackRefusal(live 2) = none, want a refusal: a pre-record binary opens schema 1 at most")
	}
}

func TestAnUnreadableSchemaRecordRefusesRatherThanGuesses(t *testing.T) {
	bin := stagedOver(t, 1)
	if err := os.WriteFile(PreviousSchemaPath(bin), []byte("not a number"), 0o600); err != nil {
		t.Fatal(err)
	}
	if why := RollbackRefusal(bin, 0); !strings.Contains(why, "cannot tell") {
		t.Errorf("RollbackRefusal = %q, want it to say it cannot tell", why)
	}

	// A record that cannot even be read -- here a directory in its place.
	bin = stagedOver(t, 1)
	os.Remove(PreviousSchemaPath(bin))
	if err := os.Mkdir(PreviousSchemaPath(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if why := RollbackRefusal(bin, 0); !strings.Contains(why, "cannot tell") {
		t.Errorf("RollbackRefusal = %q, want it to say it cannot tell", why)
	}
}

// When the record cannot be written the upgrade has still happened, and the
// caller is told both halves: installed, and what a rollback will assume.
func TestAStageWhoseSchemaRecordCannotBeWrittenSaysSo(t *testing.T) {
	dir := tempDir(t)
	bin := filepath.Join(dir, "polyemesis")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-empty directory where the record goes: it can be neither removed
	// nor renamed over.
	if err := os.MkdirAll(filepath.Join(PreviousSchemaPath(bin), "occupied"), 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(tempDir(t), "staged")
	if err := os.WriteFile(staged, []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Stage(bin, staged, hashOf(t, staged), Schema{Live: 1, Understood: 1})
	if err == nil || !strings.Contains(err.Error(), "installed") {
		t.Fatalf("Stage = %v, want an error saying the binary is nonetheless installed", err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "v2" {
		t.Errorf("live binary = %q, want the new one", b)
	}
	assertNoLitter(t, dir, "polyemesis", "polyemesis.previous", "polyemesis.previous.schema")
}
