package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* "-/OPTION PATH" READS THE OPTION'S VALUE FROM A FILE ON THE SERVER.
 *
 * FFmpeg 7.0 added the "-/" prefix to every option: "-/filter_complex graph.txt"
 * loads the filter graph from that file, "-/metadata title=/etc/x" loads a
 * metadata value from one. checkExpertArgs compared flags by their exact
 * spelling, so "-/filter_complex", "-/map" and "-/c:v" walked straight past the
 * routing and codec guards (confirmed on FFmpeg 9.0.2 during the 9.0
 * compatibility audit) -- and any option could pull the contents of a file on
 * the box into what FFmpeg sends to a platform.
 *
 * Expert mode has no use for it: every value an operator means can be typed
 * inline. So the prefix is refused outright, whatever option it is attached to,
 * on both sides.
 */
func TestExpertModeRefusesTheFileValuePrefix(t *testing.T) {
	row := &db.Destination{Name: "main"}

	for _, tc := range []struct {
		name string
		in   []string
		out  []string
	}{
		{"routing graph from a file", nil, []string{"-/filter_complex", "graph.txt"}},
		{"stream map from a file", nil, []string{"-/map", "m.txt"}},
		{"video codec from a file", nil, []string{"-/c:v", "codec.txt"}},
		{"metadata from a file", nil, []string{"-/metadata", "title=/etc/hostname"}},
		{"on the input side", []string{"-/thread_queue_size", "q.txt"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkExpertArgs(tc.in, tc.out, row)
			if err == nil {
				t.Fatalf("accepted %v %v: the -/ prefix makes FFmpeg read the value from a file "+
					"on the server, and skips every guard that matches the plain flag", tc.in, tc.out)
			}
			if !strings.Contains(err.Error(), "-/") {
				t.Errorf("refused with %q, which does not name the prefix the operator has to remove", err)
			}
		})
	}
}

// The refusal is about the option prefix, not about slashes: an ordinary value
// that happens to contain a path after a plain flag must still be accepted.
func TestExpertModeAcceptsAPathAsAPlainValue(t *testing.T) {
	row := &db.Destination{Name: "main"}
	if _, err := checkExpertArgs(nil, []string{"-metadata", "comment=see /docs"}, row); err != nil {
		t.Errorf("a plain value containing a slash was refused: %v", err)
	}
}
