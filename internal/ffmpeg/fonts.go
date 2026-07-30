package ffmpeg

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Text overlays need a font FILE, and the shipping image has none.
//
// This is not a theoretical concern. The Alpine image polyemesis ships carries
// fontconfig and zero font files, so `drawtext=text=hi` fails outright with
// "Cannot find a valid font for the family Sans". A build that relies on the
// host having fonts works on a developer laptop and fails on every deployment,
// which is the worst possible place to find out. ./scripts/test-in-docker.sh
// exists to run the tests where that is true.
//
// So the font is embedded in the binary and written to disk at startup.
// drawtext takes a path, not bytes, and cannot read from an embed.FS.

//go:embed fonts/Inter-Regular.ttf fonts/Inter-Bold.ttf fonts/LICENSE-Inter.txt
var builtinFontFS embed.FS

// FontsDirName is the directory, under the data directory, holding both the
// fonts polyemesis ships and any the operator adds.
//
// ONE directory rather than two. A built-in set in one place and operator fonts
// in another would need two resolution rules, two confinement checks and a
// precedence order to explain; a single directory needs none of that, and the
// picker is just a listing.
const FontsDirName = "fonts"

// BuiltinFonts are the file names polyemesis writes and keeps up to date.
//
// These names are RESERVED. EnsureFonts rewrites them whenever their contents
// differ from the embedded copy, so a binary upgrade also upgrades the font --
// which means an operator who replaces one of these files with their own would
// find it reverted on the next restart. Operator fonts go in under any other
// name and are never touched.
var BuiltinFonts = []string{"Inter-Regular.ttf", "Inter-Bold.ttf"}

// DefaultFont is what a text overlay uses when the operator has not chosen.
const DefaultFont = "Inter-Regular.ttf"

// fontExtensions is the allowlist. TrueType and OpenType are what FreeType
// reads and what drawtext accepts; anything else in this directory is either a
// mistake or something we should not be handing to a C font parser.
var fontExtensions = map[string]bool{".ttf": true, ".otf": true, ".ttc": true}

// EnsureFonts creates dir and writes the embedded fonts into it.
//
// Idempotent, and safe to call on every startup: a file is written only when it
// is missing or its contents differ, so the ordinary case is a read and a
// compare. Rewriting a file whose contents drifted also repairs a truncated
// font, which otherwise fails at stream time with an FFmpeg error that names
// FreeType rather than anything an operator can act on.
func EnsureFonts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the fonts directory %s: %w", dir, err)
	}
	// 0644, not 0600: these are public typefaces, not secrets. The directory
	// deliberately does NOT go through internal/fsperm for the same reason --
	// see that package for the things that do.
	for _, name := range append(append([]string{}, BuiltinFonts...), "LICENSE-Inter.txt") {
		want, err := builtinFontFS.ReadFile("fonts/" + name)
		if err != nil {
			return fmt.Errorf("read the embedded %s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		if got, err := os.ReadFile(path); err == nil && bytes.Equal(got, want) {
			continue
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// ListFonts returns the font file names in dir, sorted, built-ins included.
//
// Names rather than paths, because a name is what a setting stores and what the
// picker shows. Turning one back into a path is FontPath's job, and doing it in
// exactly one place is what keeps the confinement rule in exactly one place.
func ListFonts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error: the directory is created at startup, and a caller
			// asking before then should see "no fonts", not a failure.
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if fontExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// FontPath turns a stored font NAME into an absolute path inside dir.
//
// The name comes from a database row and becomes an FFmpeg filter argument, so
// it is confined exactly as a recording name is: a bare filename, nothing else.
//
// strings.ContainsAny over BOTH separators, spelled literally. The tempting
// `strings.ContainsRune(name, os.PathSeparator)` is a check whose meaning
// changes with GOOS -- it ignores '/' on Windows and '\' on Linux -- and this
// codebase has now shipped that mistake twice. See internal/recording.Resolve.
func FontPath(dir, name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "", name == ".", name == "..":
		return "", fmt.Errorf("invalid font name %q", name)
	case strings.ContainsAny(name, `/\`):
		return "", fmt.Errorf("font %q must be a bare filename in the fonts directory", name)
	case strings.ContainsAny(name, "\x00\n\r"):
		return "", fmt.Errorf("font name contains control characters")
	case !fontExtensions[strings.ToLower(filepath.Ext(name))]:
		return "", fmt.Errorf("font %q is not a .ttf, .otf or .ttc file", name)
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(base, name)
	// Belt and braces behind the checks above, and the one that would still
	// hold if they were ever loosened.
	if !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("font %q escapes the fonts directory", name)
	}
	if _, err := os.Stat(full); err != nil {
		// Named explicitly, because "no such file" against a path the operator
		// never typed is not a message anyone can act on.
		return "", fmt.Errorf("font %q is not in the fonts directory", name)
	}
	return full, nil
}
