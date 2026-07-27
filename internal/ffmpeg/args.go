package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"
)

// Bounds on a hand-written argument list. Neither is a security control —
// os/exec takes an argv and the kernel bounds it far above this. They exist so
// the resolved command stays something a human can read in a confirm dialog,
// which is the only thing standing between the operator and a live mistake.
const (
	MaxExtraArgsChars  = 2000
	MaxExtraArgsTokens = 64
)

// ShellMetachars are rejected outside quotes.
//
// Every one of these is inert here: the arguments become an argv passed to
// os/exec, no shell is ever forked, and a semicolon is just a semicolon. That
// is precisely why they are rejected. An argument list containing `; rm -rf /`
// is not a threat, it is a person who believes they are writing a shell line —
// and if that belief is wrong then so is their model of what the other flags
// they pasted are going to do.
//
// Deliberately absent: * ? [ ] { } ~ ! #. All four bracket forms are FFmpeg
// filter-graph syntax, `?` is the optional-stream suffix in `-map 0:a:1?`, and
// `*` and `~` appear in real paths. Rejecting them would be the restrictive
// kind of wrong this repo has already paid for three times.
const ShellMetachars = ";|&$`<>"

// SplitArgs turns one pasted line into an argv.
//
// Quoting is single or double quotes, which is how an operator gets a space
// into one argument (`-metadata "title=My Show"`). Backslash is NOT an escape:
// it is a path separator on Windows, and treating `C:\media\out` as three
// escapes would break the platform this repo just added support for.
//
// The metacharacter check happens during the scan rather than over the raw
// string, so it applies only outside quotes. `-metadata "title=Rock & Roll"` is
// a legitimate argument and is accepted; a bare `&` is not.
//
// It lives in this package rather than in the API because the engine tokenizes
// the same stored string when it starts the process. Two tokenizers would mean
// the command the operator confirmed and the command that runs could disagree
// on where one argument ends and the next begins.
func SplitArgs(raw string) ([]string, error) {
	if len(raw) > MaxExtraArgsChars {
		return nil, fmt.Errorf("too long (%d characters, limit %d)", len(raw), MaxExtraArgsChars)
	}

	var (
		out   []string
		cur   strings.Builder
		quote rune // 0, '\'' or '"'
		open  bool // cur holds a token, even if it is the empty string
	)
	flush := func() {
		if open {
			out = append(out, cur.String())
			cur.Reset()
			open = false
		}
	}

	for _, r := range raw {
		switch {
		case r == 0 || r == '\n' || r == '\r':
			// Control characters survive no round trip worth having: a newline
			// in an argv element is legal and invisible, which is the worst
			// combination for something rendered in a confirm dialog.
			return nil, fmt.Errorf("contains a control character")
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			open = true
		case r == '\'' || r == '"':
			quote = r
			// An opened quote starts a token even before any character lands
			// in it, so `-metadata ""` survives as an empty argument.
			open = true
		case r == ' ' || r == '\t':
			flush()
		case strings.ContainsRune(ShellMetachars, r):
			return nil, fmt.Errorf(
				"contains the shell metacharacter %q. These arguments are handed to FFmpeg "+
					"directly and never reach a shell, so %q would be passed through as a literal "+
					"character rather than doing what it does in a terminal. Remove it, or quote "+
					"it if it really is part of a value.", string(r), string(r))
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("has an unclosed %s quote", strconv.QuoteRune(quote))
	}
	flush()

	if len(out) > MaxExtraArgsTokens {
		return nil, fmt.Errorf("has %d arguments, limit %d", len(out), MaxExtraArgsTokens)
	}
	return out, nil
}
