// Package livevolume owns the spelling of a destination graph's named volume
// stage, `volume@<name>=volume=<level>`, which three packages need to agree on
// and none of them can import from another:
//
//   - routing emits it when it compiles a profile;
//   - the engine reads levels back out of a graph (to tell a level-only change
//     from one that must restart) and addresses a stage by name on the command
//     channel;
//   - the supervisor accepts a stage name as the only target a live command may
//     name.
//
// It was spelled in routing's format string, in ffmpeg's regexp and in the
// supervisor's regexp, with the `volume@` prefix repeated again by the engine,
// and nothing but comments said they were the same. A change to the name in one
// spot would have made the live path silently address a stage that does not
// exist.
package livevolume

import (
	"fmt"
	"regexp"
	"strconv"
)

// Prefix is what turns a stage name into the filter instance FFmpeg's command
// channel addresses.
const Prefix = "volume@"

// MaxInstance is the longest target FFmpeg reads whole: it scans the target
// with %63[^ ], so a longer one would be cut and its tail read as the time.
const MaxInstance = 63

const nameClass = `[A-Za-z0-9_]+`

var (
	stageRE    = regexp.MustCompile(Prefix + `(` + nameClass + `)=volume=([0-9.]+)`)
	instanceRE = regexp.MustCompile(`^` + Prefix + nameClass + `$`)
)

// Instance is the filter instance for a stage name: `volume@<name>`.
func Instance(name string) string { return Prefix + name }

// Stage renders the stage for a graph: `volume@<name>=volume=<level>`. level is
// already formatted, because the compiler owns how a coefficient is printed.
func Stage(name, level string) string {
	return fmt.Sprintf("%s=volume=%s", Instance(name), level)
}

// ValidInstance reports whether target is a volume filter instance and nothing
// else: the only thing a live command may name.
func ValidInstance(target string) bool { return instanceRE.MatchString(target) }

// Mask blanks the level of every named volume stage in a filter graph and
// returns the rest unchanged. A destination's restart signature is taken over
// the masked graph, so a change of level alone does not restart it and is
// applied live instead. A change of anything else still restarts.
func Mask(graph string) string {
	return stageRE.ReplaceAllString(graph, Prefix+"$1=volume=*")
}

// Levels returns stage name (without the prefix) to level for every named
// volume stage in a graph. A level that does not parse is left out rather than
// guessed at.
func Levels(graph string) map[string]float64 {
	out := map[string]float64{}
	for _, m := range stageRE.FindAllStringSubmatch(graph, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		out[m[1]] = v
	}
	return out
}
