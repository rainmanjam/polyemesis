package mqtt

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/eclipse/paho.golang/autopaho"
)

// TestClientNeverUsesTheQueuePath is the guard client.go's Queue comment cites.
//
// The comment there makes a specific claim: autopaho substitutes memory.New()
// for the nil Queue, so the nil is NOT what keeps retained telemetry from being
// replayed on reconnect. What keeps it out is that the substituted queue is
// read only by PublishViaQueue, and this package calls Publish, which bypasses
// it entirely.
//
// That is a claim about which function is called, so it is checked against the
// syntax tree rather than by running anything. There is no observable
// difference at runtime between a queue that is never read and a queue that
// does not exist -- which is exactly why this needed a guard and did not have
// one: a change to the queue path would break nothing any test could see.
//
// Proven able to fail against the committed tree by inserting this single line into
// Client.Publish in client.go:
//
//	_ = c.cm.PublishViaQueue(ctx, &autopaho.QueuePublish{Publish: &paho.Publish{Topic: topic}})
//
// It compiles against the imports already in that file, and the test reported
// the call at its line number.
func TestClientNeverUsesTheQueuePath(t *testing.T) {
	fset := token.NewFileSet()
	var publishBodies int

	for _, f := range packageSourceFiles(t) {
		// Comments are dropped: the prose in client.go names PublishViaQueue
		// several times to explain why it is not called, and a guard that could
		// be silenced by rewording a comment is not a guard.
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if node.Sel.Name == "PublishViaQueue" {
					t.Errorf("%s calls PublishViaQueue.\n"+
						"That path writes into autopaho's queue and delivers in the "+
						"background with no status. A ninety-second-old bitrate replayed "+
						"on reconnect is worse than no reading at all, because the next "+
						"tick republishes ground truth anyway. Use ConnectionManager.Publish.",
						fset.Position(node.Sel.Pos()))
				}
			case *ast.CompositeLit:
				if !isSelector(node.Type, "autopaho", "ClientConfig") {
					return true
				}
				for _, elt := range node.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Queue" {
						t.Errorf("%s configures a Queue on the autopaho client.\n"+
							"The field is left unset deliberately. Setting it is how the "+
							"queue path starts looking like the supported one to the next "+
							"reader.", fset.Position(key.Pos()))
					}
				}
			case *ast.FuncDecl:
				if recvTypeName(node) == "Client" && node.Name.Name == "Publish" {
					publishBodies++
					if !callsSelector(node, "Publish") {
						t.Errorf("%s: Client.Publish no longer calls ConnectionManager.Publish, "+
							"so this test can no longer tell which path a message takes.",
							fset.Position(node.Pos()))
					}
				}
			}
			return true
		})
	}

	// Without this the whole test passes vacuously the day Client.Publish is
	// renamed or moved, which is the failure mode of every source-level guard.
	if publishBodies != 1 {
		t.Fatalf("found %d Client.Publish declarations in the package, want exactly 1; "+
			"this test is asserting against a file layout that no longer exists", publishBodies)
	}
}

// The behaviour the queue decision buys, stated as behaviour: a publish that
// could not be transmitted comes back as an error, so telemetry drops the
// reading instead of it being delivered late and wrong.
//
// Proven able to fail against the committed tree by changing the last line of
// Client.Publish in client.go from `return err` to `return nil`.
func TestPublishWithNoBrokerFailsRatherThanBuffering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Port 1 is refused immediately on every platform we ship, so this never
	// waits on a dial. Connect does not wait for the broker by design.
	c, err := Connect(ctx, Config{
		BrokerURL: "mqtt://127.0.0.1:1",
		ClientID:  "polyemesis-queue-guard",
		Prefix:    "polyemesis",
		Instance:  "test",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.Connected() {
		t.Fatal("reported connected against a refused port; the rest of this test is meaningless")
	}

	err = c.Publish(ctx, c.Topics().Status(), QoS, true, []byte(Online))
	if err == nil {
		t.Fatal("Publish returned nil with no broker reachable.\n" +
			"The caller reads that as delivered and moves on, so the state the broker " +
			"holds silently diverges from the state polyemesis has. Returning the error " +
			"is what makes the telemetry retry rather than cache it as sent.")
	}
	if !errors.Is(err, autopaho.ConnectionDownError) {
		t.Errorf("Publish failed with %v, want autopaho.ConnectionDownError; "+
			"a different error means the message took a different path out", err)
	}
}

// packageSourceFiles is the package's own non-test sources. Tests run with the
// package directory as the working directory.
func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("no package sources found; this test is reading the wrong directory")
	}
	return out
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

func recvTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	ident, ok := t.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func callsSelector(n ast.Node, name string) bool {
	var found bool
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}
