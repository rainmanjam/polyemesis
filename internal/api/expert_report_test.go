package api

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

/* -report WRITES THE UNMASKED COMMAND LINE TO A FILE.
 *
 * FFmpeg's -report option makes the child write its own log, and the first line
 * of that file is the full argv it was invoked with -- including
 * rtmp://host/app/<streamKey>. It lands in the process's working directory,
 * outside supervisor's LogSink, which is the one place this product scrubs what
 * FFmpeg says.
 *
 * checkExpertArgs refuses -i and the routing flags because they change what the
 * pipeline DOES. This one does not change the pipeline at all; it changes where
 * the credential ends up, which is why nothing caught it. Found by codex.
 *
 * It is the operator's own key on the operator's own box, so this is not a
 * privilege boundary -- it is a promise. polyemesis says it does not write
 * stream keys to disk, and with -report accepted that was not true.
 */

func TestExpertModeRefusesTheReportFlag(t *testing.T) {
	row := &db.Destination{Name: "main"}

	for _, tc := range []struct {
		name string
		in   []string
		out  []string
	}{
		{"on the output side", nil, []string{"-report"}},
		{"on the input side", []string{"-report"}, nil},
		{"spelled with a value", nil, []string{"-report", "file=/tmp/x.log"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkExpertArgs(tc.in, tc.out, row)
			if err == nil {
				t.Fatal("accepted -report, which makes FFmpeg write its own argv — " +
					"stream key included — to a file nothing here scrubs")
			}
			if !strings.Contains(err.Error(), "report") {
				t.Errorf("refused with %q, which does not name the flag the operator "+
					"has to remove", err)
			}
		})
	}
}

// The refusal must not swallow the flags expert mode exists to allow.
func TestExpertModeStillAcceptsOrdinaryArguments(t *testing.T) {
	row := &db.Destination{Name: "main"}
	if _, err := checkExpertArgs(
		[]string{"-thread_queue_size", "1024"},
		[]string{"-b:v", "6000k", "-preset", "veryfast"},
		row,
	); err != nil {
		t.Errorf("ordinary expert arguments were refused: %v", err)
	}
}
