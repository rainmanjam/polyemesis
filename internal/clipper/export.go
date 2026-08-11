package clipper

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// ExportSubdir is where finished exports land, under the recordings directory
// and beside -- never inside -- the rolling clip buffer's own directory. The
// buffer prunes itself by count and by size, and an export a human deliberately
// made must not be swept away to make room for an instant replay nobody kept.
//
// That deliberate exclusion is exactly why exports need someone to delete them:
// nothing else ever will. See RemoveExport.
const ExportSubdir = "exports"

// ExportDirIn resolves the exports directory to an ABSOLUTE path.
//
// It has to. config.DataDir defaults to "./data", so the recordings directory
// is relative on a stock install, and Request.Validate refuses a relative
// output path -- deliberately, because the cutter is handed a path and has no
// idea what a legitimate directory is.
//
// A failure to resolve falls back to the relative path rather than to an error:
// that leaves the caller with the message they would have got anyway instead of
// inventing a second way for the same thing to break.
func ExportDirIn(recordingsDir string) string {
	dir := filepath.Join(recordingsDir, ExportSubdir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// ExportPathIn confines a stored result path to dir.
//
// The path was written by this server, not by a client, so this is a belt on
// top of braces -- but a job's params survive a database somebody edited, and
// neither the download route nor the delete route may ever be talked into
// touching an arbitrary file. Serving one leaks it; removing one destroys it,
// so the guard matters more on the delete path, not less.
func ExportPathIn(dir, stored string) (path, name string, err error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	full, err := filepath.Abs(stored)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", "", errors.New("that clip is not in the exports directory")
	}
	return full, filepath.Base(full), nil
}

// RemoveExport deletes the file a finished clip.export job produced, and
// reports whether there was one to delete.
//
// The job row is the ONLY reference to an exported file: the download route is
// keyed on the job, not on a filename, and the rolling buffer's pruning is
// pointed away from ExportSubdir on purpose. So deleting the row without this
// strands the file forever -- unservable, because the only route to it needs
// the job, and unswept, because nothing sweeps exports (#222).
//
// It is deliberately quiet about jobs that are not exports and about files that
// are already gone: this runs on the delete path and on the retention sweep,
// and neither should fail because there was nothing to clean up. It is NOT
// quiet about a path outside the exports directory, or about a filesystem that
// refused the unlink -- the first means the row was tampered with and the
// second means the disk is still filling.
func RemoveExport(exportDir string, j jobs.Job) (removed bool, err error) {
	if j.Kind != JobKind || len(j.Result) == 0 {
		return false, nil
	}
	var res JobResult
	if err := json.Unmarshal(j.Result, &res); err != nil || res.Path == "" {
		// A result that does not parse names no file. Nothing to remove, and
		// nothing an operator could act on if this were reported.
		return false, nil
	}
	path, _, err := ExportPathIn(exportDir, res.Path)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
