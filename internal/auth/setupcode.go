package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// SetupCodeFile is the name of the file, inside the data directory, that
	// holds the one-time code POST /api/v1/setup requires.
	SetupCodeFile = "setup-code"

	// SetupCodeEnv presets the code instead of minting one. It exists for an
	// operator who provisions a box unattended -- a compose file, a CI job,
	// an acceptance script -- and so already knows the code without reading a
	// log. Whoever can set a process's environment already owns the process,
	// so this grants nothing that reading the file would not.
	SetupCodeEnv = "POLYEMESIS_SETUP_CODE"

	// setupCodeMinChars is the shortest code accepted, counted after
	// normalising. A minted code is sixteen; a preset one shorter than twelve
	// is refused rather than quietly used, because a short code on a port the
	// whole network can reach is exactly the hole this closes.
	setupCodeMinChars = 12

	// setupCodeAlphabet has 32 symbols, so one random byte maps to one symbol
	// with no modulo bias, and it leaves out 0/O and 1/I, which are the pairs
	// someone copying a code off a terminal gets wrong.
	setupCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// SetupCode is the one-time code that proves the person creating the first
// admin account can read this box's files or its log.
//
// WHY IT EXISTS. POST /api/v1/setup is unauthenticated by necessity -- there is
// no account yet -- and GET /api/v1/setup tells anyone who asks that the install
// still needs one. On a fresh install whose port is open, which is what
// install.sh produces, the first stranger to reach the port used to become the
// admin. The code moves the proof from "got there first" to "can read
// <dataDir>/setup-code or the server's log", which is the same bar -reset-admin
// sets.
//
// A nil *SetupCode is a valid value meaning "no code is pending": every method
// answers as though setup were already done. That is the fail-closed direction
// -- a server built without one refuses setup rather than accepting it.
type SetupCode struct {
	mu      sync.Mutex
	display string // as printed: grouped, upper-case
	want    string // normalised, what an attempt is compared against
	path    string
	reused  bool
	preset  bool
}

// PrepareSetupCode returns the code a fresh install requires, or nil when the
// install already has an admin.
//
// A RESTART BEFORE SETUP KEEPS THE CODE. An existing, well-formed file is read
// back rather than replaced, so the code an operator copied out of the log an
// hour ago still works after the service restarts -- a code that changed on
// every restart would send them back to the log for each one, and journalctl
// shows the old line first. A file that is missing, empty or too short is
// replaced with a freshly minted code.
//
// preset, when non-empty, is used instead (see SetupCodeEnv) and written to the
// file, so "where is the code" has one answer however it was chosen.
//
// When hasUser is true, any leftover file is removed: a code for an install that
// is already claimed is at best confusing and at worst read by someone as a
// credential that still means something.
func PrepareSetupCode(dataDir string, hasUser bool, preset string) (*SetupCode, error) {
	path := filepath.Join(dataDir, SetupCodeFile)
	if hasUser {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remove leftover %s: %w", path, err)
		}
		return nil, nil
	}

	c := &SetupCode{path: path}
	switch {
	case strings.TrimSpace(preset) != "":
		if len(normaliseSetupCode(preset)) < setupCodeMinChars {
			return nil, fmt.Errorf("%s is shorter than %d characters; use a longer code or unset it to have one generated",
				SetupCodeEnv, setupCodeMinChars)
		}
		c.display, c.preset = strings.TrimSpace(preset), true
	default:
		if b, err := os.ReadFile(path); err == nil && len(normaliseSetupCode(string(b))) >= setupCodeMinChars {
			c.display, c.reused = strings.TrimSpace(string(b)), true
		} else {
			code, err := mintSetupCode()
			if err != nil {
				return nil, err
			}
			c.display = code
		}
	}
	c.want = normaliseSetupCode(c.display)
	if err := writeSetupCode(path, c.display); err != nil {
		return nil, err
	}
	return c, nil
}

// Pending reports whether a code is still waiting to be used.
func (c *SetupCode) Pending() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.want != ""
}

// Matches reports whether got is the pending code. Constant-time in the
// comparison itself, and case-, space- and dash-insensitive, because the code
// is read off a terminal and typed by hand.
func (c *SetupCode) Matches(got string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	want := c.want
	c.mu.Unlock()
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(normaliseSetupCode(got)), []byte(want)) == 1
}

// Consume retires the code once the admin exists: it stops matching at once,
// and the file is deleted. The in-memory half cannot fail; a file that cannot
// be removed is reported, and the next boot removes it anyway because by then
// there is a user.
func (c *SetupCode) Consume() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.want = ""
	c.mu.Unlock()
	if err := os.Remove(c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Code is the code as it should be shown to the operator.
func (c *SetupCode) Code() string {
	if c == nil {
		return ""
	}
	return c.display
}

// Path is the file the code was written to.
func (c *SetupCode) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Reused reports whether the code was read back from an earlier boot's file.
func (c *SetupCode) Reused() bool { return c != nil && c.reused }

// Preset reports whether the code came from SetupCodeEnv.
func (c *SetupCode) Preset() bool { return c != nil && c.preset }

func normaliseSetupCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t', '\r', '\n':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mintSetupCode returns sixteen symbols -- 80 random bits -- in four groups of
// four. Against a per-address throttle that is far past guessable, and it is
// still short enough to type.
func mintSetupCode() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint setup code: %w", err)
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(setupCodeAlphabet[int(v)%len(setupCodeAlphabet)])
	}
	return b.String(), nil
}

// writeSetupCode writes the code readable by the owner only. Through a temp
// file and a rename, and chmod'd explicitly, because os.WriteFile on an
// existing file keeps that file's mode -- a leftover created 0644 would stay
// 0644 and the code would be readable by every account on the box.
func writeSetupCode(path, code string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), SetupCodeFile+".*")
	if err != nil {
		return fmt.Errorf("write setup code: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(code + "\n")
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write setup code: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write setup code: %w", err)
	}
	return nil
}
