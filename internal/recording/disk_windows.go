//go:build windows

package recording

// diskFree is unimplemented on Windows; the recordings page simply omits the
// free-space figure there. polyemesis targets Unix.
func diskFree(path string) (free, total uint64, err error) {
	return 0, 0, nil
}
