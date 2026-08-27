package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// ------------------------------------------------------------------- expert
//
// Expert mode: hand-edited FFmpeg arguments, per destination.
//
// Restreamer lets an advanced user edit the process outright. polyemesis will
// not go that far — the generated command carries the whole product, and a free
// text box replacing it means a destination that no longer routes the audio the
// operator selected. What is offered instead is an escape hatch with edges:
// two strings appended to the generated command, one before the input and one
// before the output, everything else untouched.
//
// Three things make it safe enough to ship:
//
//   - The FULL resolved command is rendered before anything is saved. Someone
//     pasting flags from a forum thread into a live stream deserves to see the
//     exact argv that will run, not a diff of the part they typed.
//   - The arguments are validated rather than trusted. os/exec never goes near
//     a shell, so a semicolon here is inert — but it means the author believes
//     they are writing a shell line, and everything else they believe is now
//     suspect too.
//   - A dry run puts the resolved command in front of FFmpeg with the output
//     discarded, so a misspelt flag is a message in the editor rather than a
//     destination that crash-loops after going live.
//
// The dry run's verdict is deliberately three-valued. An unreachable ingest is
// not a verdict on the arguments, and reporting it as one would be the same
// restrictive-direction mistake the SRT probe and the encoder check each made:
// unless FFmpeg positively complained about an OPTION, expert mode says so and
// gets out of the way.

// expertArgs is one destination's hand-written additions.
type expertArgs struct {
	// InputArgs are spliced in immediately before -i. FFmpeg applies input
	// options to the next input that follows them, so anywhere else is
	// silently a no-op.
	InputArgs string `json:"inputArgs"`
	// OutputArgs are spliced in immediately before the output target, which is
	// where FFmpeg expects options for the output that follows.
	OutputArgs string `json:"outputArgs"`
	// AckReencode is the operator saying, in as many words, that they know an
	// argument here overrides something the product otherwise guarantees. It is
	// stored rather than treated as a one-shot confirmation so that a later
	// edit which keeps the same override does not silently lose the record of
	// who agreed to it.
	AckReencode bool `json:"ackReencode"`
	// UpdatedAt is zero when nothing has ever been saved for this destination,
	// which is the COMMON case: expert mode is off on almost every destination.
	//
	// `omitzero`, not `omitempty`, because `omitempty` DOES NOTHING ON A
	// time.Time -- encoding/json has no empty case for a struct. So the tag
	// read as a guard and was decoration, and every destination that had never
	// been given expert arguments served "0001-01-01T00:00:00Z". A non-empty
	// string that parses cleanly, so a client truthiness guard passed and the
	// expert panel showed "last edited 12/31/1, 16:07:02" for a destination
	// nobody had ever edited.
	//
	// `omitzero` (Go 1.24) drops the key: the shape cannot put an instant on
	// the wire that never happened.
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

// set reports whether this destination has any expert arguments at all. A row
// of two empty strings is indistinguishable from no row, and both must render
// as "expert mode off".
func (e expertArgs) set() bool {
	return strings.TrimSpace(e.InputArgs) != "" || strings.TrimSpace(e.OutputArgs) != ""
}

// ------------------------------------------------------------------ storage
//
// Three columns on `destinations`, not a sidecar table.
//
// The engine has to fold these into the destination's restart signature, and a
// signature assembled from two different reads of two different tables is a
// signature that can be assembled from a torn pair. One row, one read, one
// answer to "what command does this destination run".

// expertArgsOf reads the saved arguments off a destination row. A destination
// that has never opted in carries two empty strings, which is the zero value
// and is not an error.
func expertArgsOf(row *db.Destination) expertArgs {
	e := expertArgs{
		InputArgs:   row.ExtraInputArgs,
		OutputArgs:  row.ExtraOutputArgs,
		AckReencode: row.ExpertAckReencode,
	}
	// UpdatedAt is the destination's, because these fields now live on it: an
	// edit to the arguments is an edit to the destination.
	if e.set() {
		e.UpdatedAt = row.UpdatedAt
	}
	return e
}

// clearExpertArgs strips hand-written arguments off a decoded request body.
//
// The ordinary destination endpoints call it because decodeJSON uses
// DisallowUnknownFields, which means these fields became acceptable on
// POST /destinations and PUT /destinations/{id} the moment they landed on
// db.Destination. Only the expert routes enforce the confirm step, the guard
// acknowledgement and the dry run; a destination create carrying
// extraOutputArgs would be a way past all three.
func clearExpertArgs(row *db.Destination) {
	row.ExtraInputArgs = ""
	row.ExtraOutputArgs = ""
	row.ExpertAckReencode = false
}

// saveExpertArgs writes the arguments back through the destination row.
//
// The row is re-read rather than taking the caller's copy, so a concurrent edit
// to the name or the URL is not silently reverted by an expert-mode save.
func (s *Server) saveExpertArgs(id int64, e expertArgs) (*db.Destination, error) {
	row, err := s.store.GetDestination(id)
	if err != nil {
		return nil, err
	}
	row.ExtraInputArgs = e.InputArgs
	row.ExtraOutputArgs = e.OutputArgs
	row.ExpertAckReencode = e.AckReencode
	return s.store.UpdateDestination(row)
}

// --------------------------------------------------------------- validation

// expertParseError names the field as well as the problem, because the editor
// has two boxes and "unbalanced quote" without a side is a scavenger hunt.
func expertParseError(field, format string, a ...any) error {
	return fmt.Errorf("%s: %s", field, fmt.Sprintf(format, a...))
}

// splitExpertArgs turns one pasted line into an argv, tagging any problem with
// the field it came from — the editor has two boxes, and "unbalanced quote"
// without a side is a scavenger hunt.
//
// The tokenizer itself is ffmpeg.SplitArgs, shared with the engine so that the
// command the operator confirms and the command the engine spawns cannot
// disagree about where one argument ends.
func splitExpertArgs(raw, field string) ([]string, error) {
	argv, err := ffmpeg.SplitArgs(raw)
	if err != nil {
		return nil, expertParseError(field, "%v", err)
	}
	return argv, nil
}

// videoCodecFlag reports whether flag decides what happens to video. `-c` and
// `-codec` are included because they set every stream's codec, video included:
// `-c libx264` is `-c:v libx264` wearing a hat.
func videoCodecFlag(flag string) bool {
	switch flag {
	case "-c", "-codec", "-vcodec", "-c:v", "-codec:v":
		return true
	}
	// Stream-indexed forms, -c:v:0 and friends.
	return strings.HasPrefix(flag, "-c:v:") || strings.HasPrefix(flag, "-codec:v:")
}

// routingFlag reports whether flag can replace the audio routing graph. These
// are the three that decide which of the ingest's tracks reach the output, and
// a second one appended after ours is not additive — it is a different answer
// to the same question, and FFmpeg will honour one of them.
func routingFlag(flag string) bool {
	switch flag {
	case "-map", "-filter_complex", "-lavfi", "-filter_complex_script":
		return true
	}
	return false
}

// expertGuard is one reason a set of arguments needs the operator to say yes.
type expertGuard struct {
	// Arg is the flag that triggered it, quoted back so the operator can find
	// it in what they typed.
	Arg string `json:"arg"`
	// Reason is what that flag would do to this specific destination.
	Reason string `json:"reason"`
}

// checkExpertArgs validates both sides and reports what needs acknowledging.
//
// Errors are refusals: nothing acknowledges them away. Guards are the softer
// class — real overrides of a real guarantee, permitted once the operator has
// said so explicitly. The split matters: a second -i renumbers every input
// stream, so `[0:a:3]` in the routing graph starts pointing at a different
// track, and there is no version of that the operator meant.
// reportFlagRefusal is the message for -report, which is refused on both sides.
//
// EVERY OTHER REFUSAL HERE IS ABOUT WHAT THE PIPELINE DOES. -report changes
// nothing about the stream and changes where the CREDENTIAL ends up: FFmpeg
// writes its own log file whose first line is the argv it was invoked with,
// including rtmp://host/app/<streamKey>, into the working directory — outside
// supervisor's LogSink, which is the only place this product scrubs what FFmpeg
// says. Found by codex during the v0.7.0 pre-tag review.
//
// It is the operator's own key on their own box, so this is a broken promise
// rather than a privilege boundary. The promise is still worth keeping.
const reportFlagRefusal = "-report makes FFmpeg write its own log file, and the first line of " +
	"that file is the full command line — which contains this destination's stream key. " +
	"It would be written to disk unmasked, outside the log polyemesis scrubs. Remove it; " +
	"the resolved command is available on this page without it"

func checkExpertArgs(in, out []string, row *db.Destination) (guards []expertGuard, err error) {
	for _, a := range append(append([]string{}, in...), out...) {
		if a == "-report" {
			return nil, errors.New(reportFlagRefusal)
		}
	}
	for _, a := range in {
		if a == "-i" {
			return nil, errors.New("input args: -i adds a second input, which renumbers every " +
				"stream the routing graph refers to. The destination's input is the relay and " +
				"cannot be changed here")
		}
		if routingFlag(a) {
			return nil, fmt.Errorf("input args: %s belongs on the output side, where the routing "+
				"graph is. Put it in the output arguments if you really mean it", a)
		}
	}

	// The video guarantee this destination is making right now. Passthrough
	// means -c:v copy off the ingest; a rendition means -c:v copy off a shared
	// encode that already happened. Overriding it re-encodes in the first case
	// and double-encodes in the second, and both deserve to be said out loud.
	promise := "the source video is copied through untouched (-c:v copy)"
	if row.RenditionID != nil {
		promise = fmt.Sprintf("this destination copies video from rendition %d, which has already "+
			"encoded it once", *row.RenditionID)
	}

	for i, a := range out {
		switch {
		case a == "-i":
			return nil, errors.New("output args: -i adds an input, not an output. " +
				"The destination's input is the relay and cannot be changed here")

		case videoCodecFlag(a):
			// `-c:v copy` is what the generated command already says. Repeating
			// it changes nothing and needs no ceremony.
			if i+1 < len(out) && out[i+1] == "copy" {
				continue
			}
			guards = append(guards, expertGuard{
				Arg: a,
				Reason: fmt.Sprintf("%s overrides the video codec. Today %s. Re-encoding video "+
					"here costs CPU on every frame and degrades what the platform receives.", a, promise),
			})

		case routingFlag(a):
			guards = append(guards, expertGuard{
				Arg: a,
				Reason: fmt.Sprintf("%s decides which of the ingest's audio tracks reach this "+
					"destination. The generated command already answers that from the routing "+
					"profile, and a second answer can silently override it — which is the one "+
					"failure this product cannot have.", a),
			})
		}
	}
	return guards, nil
}

// ------------------------------------------------------- command resolution

// resolvedCommand is the exact argv, rendered for a human.
type resolvedCommand struct {
	Bin  string   `json:"bin"`
	Argv []string `json:"argv"`
	// Command is Argv shell-quoted for display. It is for reading, not for
	// pasting into a terminal — the running process never sees a shell.
	Command string `json:"command"`
	// Live is true when this was taken from the destination's running process,
	// which makes every value in it real. When false the command was rebuilt
	// from the saved configuration and Note says which parts are stand-ins.
	Live bool   `json:"live"`
	Note string `json:"note,omitempty"`
}

// placeholderRelayURL stands in for the loopback port the hub assigns when a
// destination starts. Port 0 is not a port anything binds, which is the point:
// nobody should read a preview and believe they can run it as printed.
const placeholderRelayURL = "udp://127.0.0.1:0"

// quoteArgv renders an argv the way the monitoring page renders a live one, so
// the preview and the process card do not disagree about the same command.
func quoteArgv(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, bin)
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\"'|&;<>()$`\\") {
			parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// destinationBaseArgv returns the command this destination runs, or would run.
//
// The running process is preferred and is not a convenience: it carries the
// relay port the hub actually assigned and the file path the recordings manager
// actually resolved, which nothing outside the engine can reproduce. The
// rebuilt form is the fallback for a stopped destination, and says so.
//
// THIS ONE IS DELIBERATELY NOT REDACTED, and the difference from every other
// egress of these bytes is worth stating so nobody "fixes" it later.
//
// Process.CommandString, Process.Logs, Process.appendLog and Status.LastError
// are all masked inside supervisor now, because no caller of those wanted the
// raw form and two of them leave the process entirely with no principal
// attached: the on-disk process.log and a RETAINED MQTT topic. Args() stays raw
// for callers that must reason about the arguments themselves, and this is that
// caller.
//
// What those four do is NOT what the previous version of this comment implied.
// They do not run alerts.Redact and call it masked -- that is what shipped, and
// it left `-rtmp_conn S:<key>` and `Authorization:Bearer\ <key>` in the clear on
// three read-reachable egresses. They remove the destination's EXACT credential
// literals first, declared on the Spec, and run Redact only as a residual pass
// afterwards. This route is the deliberate exception to that, and it is
// admin-only for exactly that reason.
//
// Expert mode's whole contract is that the command the operator confirms cannot
// drift from the one that runs -- see resolveExpertCommand, which splices
// through the same function the engine does for exactly that reason. A masked
// target would show them a command that is NOT the one that will run, while
// telling them it is. That is a worse failure than the disclosure: it breaks
// the approval this screen exists to obtain.
//
// The exposure is bounded instead BY WHO CAN ASK. What used to stand here
// argued that redaction would remove nothing, "because the same caller can
// already read streamKey and backupStreamKey in cleartext from
// GET /destinations". Token scopes falsified that premise: a read-scoped bearer
// is now a lower-privileged principal inside the same requireAuth boundary, and
// it is refused both streamKey on /destinations and these routes outright --
// GET /destinations/{id}/expert and POST /destinations/{id}/expert/preview are
// in readScopeDeniedPatterns. A session and an admin token, which can rotate
// the key anyway, still get the exact command.
//
// Do not "fix" this by masking the argv. The next reader who is tempted should
// read that list's comment first.
func (s *Server) destinationBaseArgv(row *db.Destination) (bin string, base []string, live bool, note string, err error) {
	// A FIELD read, so the nil check is not optional the way it is on
	// HasFilter: only Tools' methods are nil-receiver safe. An install that
	// reported no FFmpeg leaves the binary empty here, which is what the
	// caller already renders as "this command cannot be run".
	if tools := s.tools(); tools != nil {
		bin = tools.FFmpeg
	}

	// THE REFUSAL LIVES HERE rather than on the three read routes, because
	// this is what needs the engine: the process list this searches, and the
	// arriving stream the profile below is compiled against. Three of the five
	// expert routes write nothing, so the router's requireSource list does not
	// carry them, and an operator asking what command a destination would run
	// on an install with no programme gets the same sentence the guarded two
	// give rather than a 409 about the destination.
	//
	// Returned as an error, not rendered: this is reached from five handlers
	// and none of them has told it whether a response has been written yet.
	e := s.engOrNil()
	if e == nil {
		return bin, nil, false, "", errNoSource
	}

	want := fmt.Sprintf("dest:%d", row.ID)
	for _, p := range e.Processes() {
		if p.Name() != want {
			continue
		}
		argv := p.Args()
		if len(argv) <= 1 {
			continue
		}
		// The running process was started WITH whatever was saved at the time,
		// because the engine splices them. Peeling those back off is what makes
		// a preview of a candidate edit show the candidate instead of the
		// candidate stacked on top of the previous one.
		//
		// Parse errors are ignored deliberately: arguments that no longer parse
		// cannot be the ones this process was started with, so there is nothing
		// to strip, and refusing to show the operator their live command line is
		// the worst possible answer to "what is running right now".
		applied := expertArgsOf(row)
		oldIn, _ := splitExpertArgs(applied.InputArgs, "input args")
		oldOut, _ := splitExpertArgs(applied.OutputArgs, "output args")
		return argv[0], ffmpeg.StripExtraArgs(argv[1:], oldIn, oldOut), true, "", nil
	}

	compiled, cerr := routing.Compile(row.Profile, e.Source())
	if cerr != nil {
		// A profile that does not compile is a routing problem, not an expert
		// mode one, and it has its own editor. Say which so the operator does
		// not go looking for a typo in the arguments they just pasted.
		return bin, nil, false, "", fmt.Errorf("this destination's routing profile does not "+
			"compile, so no command can be built for it: %w", cerr)
	}

	note = "This destination is not running, so the command was rebuilt from its saved " +
		"configuration. The relay URL shown is a placeholder: the real loopback port is " +
		"assigned when the destination starts."
	target := row.Target()
	// The audio-only kind shares the file kind's confinement whenever its
	// target is a path rather than an Icecast URL, so it has to share the note.
	if row.Kind == db.DestFile || (row.Kind == db.DestAudio && !strings.Contains(row.URL, "://")) {
		target = row.URL
		note += " The output path is shown as configured; it is resolved inside the " +
			"recordings directory at start."
	}

	// This is the SECOND place a ffmpeg.DestSpec is built -- the engine's
	// destSpecFor is the other -- and it has always omitted Audio and Transport,
	// so the preview of a destination using either already differs from the
	// command that runs. CopyAudio is added here anyway, because the difference
	// it makes is not a missing flag but a whole shape: without it the operator
	// previewing a copy destination is shown a filter graph and an encoder that
	// will not be there. Unifying the two construction sites is a separate
	// change and has its own follow-up issue.
	base = ffmpeg.DestinationArgs(ffmpeg.DestSpec{
		Kind:          ffmpeg.DestKind(row.Kind),
		Target:        target,
		RelayURL:      placeholderRelayURL,
		FilterComplex: compiled.FilterComplex,
		AudioOutLabel: compiled.OutLabel,
		AudioBitrate:  row.AudioBitrate,
		SampleRate:    row.Profile.SampleRate,
		CopyVideo:     true,
		VideoDelayMS:  compiled.VideoDelayMS,
		CopyAudio:     row.Audio.Copy,
		AudioTracks:   compiled.Tracks,
	})
	return bin, base, false, note, nil
}

// resolveExpertCommand builds the full command the operator is being asked to
// approve: generated arguments, their additions, in the positions FFmpeg reads
// them from.
func (s *Server) resolveExpertCommand(row *db.Destination, in, out []string) (resolvedCommand, error) {
	bin, base, live, note, err := s.destinationBaseArgv(row)
	if err != nil {
		return resolvedCommand{}, err
	}
	// The same splice DestinationArgs performs, from the same function, so the
	// command the operator confirms cannot drift from the one that runs.
	argv := ffmpeg.SpliceExtraArgs(base, in, out)
	return resolvedCommand{
		Bin:     bin,
		Argv:    argv,
		Command: quoteArgv(bin, argv),
		Live:    live,
		Note:    note,
	}, nil
}

// writeExpertCommandError renders a resolveExpertCommand failure.
//
// Everything this can fail with is a 409 -- a destination whose routing profile
// will not compile, which is a conflict between what is stored and what can be
// built -- except one. "There is no programme at all" is not a statement about
// this destination, and answering 409 would send the operator to the routing
// editor of a destination that is fine.
func writeExpertCommandError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoSource) {
		writeNoSource(w)
		return
	}
	writeError(w, http.StatusConflict, err.Error())
}

// ----------------------------------------------------------------- dry run

// dryRunTimeout bounds the whole attempt. FFmpeg is given -t 1 so it should
// exit in about a second when the input is live; this is the backstop for the
// case where it is not and the protocol sits waiting.
const dryRunTimeout = 12 * time.Second

// dryRunVerdict is deliberately three-valued.
type dryRunVerdict string

const (
	// dryRunOK: FFmpeg accepted the command and ran it.
	dryRunOK dryRunVerdict = "ok"
	// dryRunInvalid: FFmpeg positively rejected an option. This is the only
	// verdict that should stop anybody.
	dryRunInvalid dryRunVerdict = "invalid"
	// dryRunInconclusive: something else went wrong — most often no live
	// ingest to read. Nothing was demonstrated about the arguments, and saying
	// otherwise would be the restrictive-direction lie this repo keeps
	// re-learning.
	dryRunInconclusive dryRunVerdict = "inconclusive"
)

type dryRunResult struct {
	Verdict dryRunVerdict `json:"verdict"`
	// Message is FFmpeg's own words when it had any, because "Unrecognized
	// option 'preseat'" fixes itself and "the dry run failed" does not.
	Message string `json:"message,omitempty"`
	// Command is what was actually run, which is the resolved command with its
	// output replaced. Shown so nobody has to trust that claim.
	Command string   `json:"command"`
	Argv    []string `json:"argv"`
	// Output is FFmpeg's combined output, truncated. The classifier reads a
	// handful of phrases; the operator can read the rest.
	Output string `json:"output,omitempty"`
}

// optionErrorPhrases are the things FFmpeg says when it has rejected an
// ARGUMENT, as opposed to failed to do a job. Only these turn into a refusal.
//
// The list is a whitelist for saying no, not for saying yes: a phrase missing
// from it costs an inconclusive verdict, which lets the operator proceed. A
// phrase wrongly in it would refuse a command that works, which is the failure
// mode that actually hurts.
var optionErrorPhrases = []string{
	"unrecognized option",
	"unknown option",
	"option not found",
	"error splitting the argument list",
	"unable to parse option value",
	"error parsing option",
	"error applying option",
	"invalid stream specifier",
	"trailing options were found on the commandline",
	"no such filter",
	"error parsing filterchain",
	"error initializing filter",
	"unknown encoder",
	"unknown decoder",
	"unknown muxer",
	"requested output format",
	"could not write header", // a codec the container will not carry
	"codec not currently supported in container",
	"missing argument for option",
}

// dryRunArgv turns a resolved command into one that publishes nowhere.
//
// Three changes, and nothing else:
//
//   - The output target is dropped and replaced with `-f null -`, so the muxer
//     is exercised without a byte leaving the machine and no platform ever sees
//     a connection it did not expect. The trailing -f wins over the generated
//     one, which is FFmpeg's own rule for repeated output options.
//   - `-t 1` bounds the run when the input is live.
//   - `-progress pipe:1` goes, because the dry run reads FFmpeg's output to
//     find out what went wrong and the progress stream would bury the one line
//     that matters under a hundred key=value pairs.
func dryRunArgv(argv []string) []string {
	if len(argv) == 0 {
		return nil
	}
	out := make([]string, 0, len(argv)+5)
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-progress" && i+1 < len(argv)-1 {
			i++ // skip the destination too
			continue
		}
		out = append(out, argv[i])
	}
	return append(out, "-t", "1", "-f", "null", "-")
}

// runDryRun executes the command with its output discarded and classifies what
// came back.
func runDryRun(ctx context.Context, bin string, argv []string) dryRunResult {
	res := dryRunResult{Argv: dryRunArgv(argv)}
	res.Command = quoteArgv(bin, res.Argv)

	if bin == "" || len(res.Argv) == 0 {
		res.Verdict = dryRunInconclusive
		res.Message = "no FFmpeg binary is configured, so nothing could be tried"
		return res
	}

	// The caller's cancellation is dropped on purpose. A dry run that is
	// abandoned when the browser tab closes would leave the operator with no
	// verdict and a spawned FFmpeg; the timeout below bounds it either way.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dryRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, res.Argv...)
	// Killing FFmpeg is not the same as getting its output pipe back — a child
	// inherits it and CombinedOutput blocks until every holder is gone. Without
	// WaitDelay the timeout above is a suggestion. Learned in probe_encoders.go.
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()
	res.Output = truncateOutput(string(output), 4000)

	switch {
	case err == nil:
		res.Verdict = dryRunOK
		res.Message = "FFmpeg accepted every argument and ran the command."
		return res
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		// It got far enough to sit waiting, which means the argument list
		// parsed. That is most of what the dry run was asked to prove.
		res.Verdict = dryRunInconclusive
		res.Message = fmt.Sprintf("FFmpeg accepted the arguments and was still running after %s, "+
			"so it was stopped. Nothing here says the command is wrong.", dryRunTimeout)
		return res
	}

	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		res.Verdict = dryRunInconclusive
		res.Message = fmt.Sprintf("FFmpeg could not be started (%v), so the arguments were "+
			"never checked.", err)
		return res
	}

	if line, ok := findOptionError(string(output)); ok {
		res.Verdict = dryRunInvalid
		res.Message = line
		return res
	}

	res.Verdict = dryRunInconclusive
	res.Message = "FFmpeg exited with an error, but not one about an argument — most often " +
		"that is no live ingest to read from. The arguments were not shown to be wrong."
	if line := firstErrorLine(string(output)); line != "" {
		res.Message += " FFmpeg said: " + line
	}
	return res
}

// findOptionError looks for a line that is FFmpeg objecting to an argument.
func findOptionError(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		for _, p := range optionErrorPhrases {
			if strings.Contains(lower, p) {
				return truncateOutput(line, 300), true
			}
		}
	}
	return "", false
}

// firstErrorLine returns something to quote when the failure was not about an
// argument, so an inconclusive verdict still carries a clue.
func firstErrorLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncateOutput(line, 300)
		}
	}
	return ""
}

func truncateOutput(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// ---------------------------------------------------------------- handlers

// expertRequest is the shape of every write and every check.
type expertRequest struct {
	InputArgs   string `json:"inputArgs"`
	OutputArgs  string `json:"outputArgs"`
	AckReencode bool   `json:"ackReencode"`
	// Confirm is required by PUT only. It is the operator saying they read the
	// resolved command, and it is a separate field from AckReencode on purpose:
	// one is "I have seen what will run", the other is "I know it breaks a
	// specific guarantee", and a client must not be able to satisfy both by
	// setting one.
	Confirm bool `json:"confirm"`
}

// expertResponse is what the editor renders: the arguments, the full command
// they produce, and everything standing between them and being applied.
type expertResponse struct {
	DestinationID int64      `json:"destinationId"`
	Args          expertArgs `json:"args"`
	// Enabled reports whether this destination currently has expert arguments
	// saved, so the UI can render an off state rather than two empty boxes.
	Enabled bool            `json:"enabled"`
	Command resolvedCommand `json:"command"`
	// Guards are overrides that need AckReencode set. Non-empty with
	// AckReencode false is why a PUT would be refused.
	Guards []expertGuard `json:"guards,omitempty"`
	// Passthrough is whether this destination copies the ingest's video
	// directly, which is what makes a -c:v override serious.
	Passthrough bool `json:"passthrough"`
	// Applied is false on a preview and true on a read or a successful write,
	// so the editor can label what it is looking at.
	Applied bool `json:"applied"`
	// Warning is set when the saved arguments exist but are not in the running
	// process. See handleGetExpert.
	Warning string `json:"warning,omitempty"`
}

// parseExpertRequest validates both argument strings and the guards they trip.
// It does not touch the database or the engine, so a preview and a write agree
// about what is acceptable by construction.
func parseExpertRequest(req expertRequest, row *db.Destination) (in, out []string, guards []expertGuard, err error) {
	if in, err = splitExpertArgs(req.InputArgs, "input args"); err != nil {
		return nil, nil, nil, err
	}
	if out, err = splitExpertArgs(req.OutputArgs, "output args"); err != nil {
		return nil, nil, nil, err
	}
	guards, err = checkExpertArgs(in, out, row)
	return in, out, guards, err
}

// handleGetExpert reports the current arguments and the command they produce.
func (s *Server) handleGetExpert(w http.ResponseWriter, r *http.Request) {
	// THIS RESPONSE CONTAINS THE STREAM KEY, AND IT DIVERGES BY PRINCIPAL.
	//
	// readScopeDeniedPatterns lists this route precisely because, in redact.go's
	// words, "its response is the resolved FFmpeg argv, and the argv contains the
	// destination's stream key". So the same URL answers 200-with-the-key for a
	// session or admin token and 403 for a read-scoped one — the exact shape
	// principalVaryingResponse exists for, and it was never called here.
	//
	// The failure needs no bug in polyemesis. An operator fronts the box with
	// nginx and `proxy_cache_valid 200 10m` — the deployment redact.go names when
	// it calls these headers "required rather than defensive". The console loads
	// this URL, nginx stores the 200 keyed on the path alone because nothing says
	// Vary, and a read-scoped monitoring token then gets the stream key served
	// from cache without the request ever reaching requireScope.
	principalVaryingResponse(w)

	id, row, ok := s.expertDestination(w, r)
	if !ok {
		return
	}
	saved := expertArgsOf(row)
	in, out, guards, err := parseExpertRequest(expertRequest{
		InputArgs:   saved.InputArgs,
		OutputArgs:  saved.OutputArgs,
		AckReencode: saved.AckReencode,
	}, row)
	if err != nil {
		// Saved arguments that no longer validate are still shown. The rules
		// can tighten between releases, and hiding what is stored would leave
		// the operator unable to see, let alone fix, what their server is
		// carrying.
		writeJSON(w, http.StatusOK, expertResponse{
			DestinationID: id,
			Args:          saved,
			Enabled:       saved.set(),
			Passthrough:   row.RenditionID == nil,
			Applied:       true,
			Warning:       "the saved arguments no longer validate: " + err.Error(),
		})
		return
	}

	cmd, err := s.resolveExpertCommand(row, in, out)
	if err != nil {
		writeExpertCommandError(w, err)
		return
	}

	resp := expertResponse{
		DestinationID: id,
		Args:          saved,
		Enabled:       saved.set(),
		Command:       cmd,
		Guards:        guards,
		Passthrough:   row.RenditionID == nil,
		Applied:       true,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handlePreviewExpert validates a candidate edit and renders the full command
// it would produce, without saving anything.
//
// This is the endpoint the confirm step is built on. The editor calls it, shows
// the operator the exact argv, and only then offers to apply — a person pasting
// flags they have not read into a live stream should at least be shown the
// whole line they are signing.
func (s *Server) handlePreviewExpert(w http.ResponseWriter, r *http.Request) {
	id, row, ok := s.expertDestination(w, r)
	if !ok {
		return
	}
	var req expertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, out, guards, err := parseExpertRequest(req, row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmd, err := s.resolveExpertCommand(row, in, out)
	if err != nil {
		writeExpertCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, expertResponse{
		DestinationID: id,
		Args: expertArgs{
			InputArgs:   req.InputArgs,
			OutputArgs:  req.OutputArgs,
			AckReencode: req.AckReencode,
		},
		Enabled:     strings.TrimSpace(req.InputArgs) != "" || strings.TrimSpace(req.OutputArgs) != "",
		Command:     cmd,
		Guards:      guards,
		Passthrough: row.RenditionID == nil,
		Applied:     false,
	})
}

// handlePutExpert saves the arguments, once the operator has confirmed.
func (s *Server) handlePutExpert(w http.ResponseWriter, r *http.Request) {
	id, row, ok := s.expertDestination(w, r)
	if !ok {
		return
	}
	var req expertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, out, guards, err := parseExpertRequest(req, row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest,
			"expert arguments must be confirmed. Fetch the resolved command from "+
				"POST /destinations/{id}/expert/preview, read it, then repeat this request "+
				"with \"confirm\": true")
		return
	}
	if len(guards) > 0 && !req.AckReencode {
		writeError(w, http.StatusBadRequest, guardRefusal(guards))
		return
	}

	updated, err := s.saveExpertArgs(id, expertArgs{
		InputArgs:   req.InputArgs,
		OutputArgs:  req.OutputArgs,
		AckReencode: req.AckReencode,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	saved := expertArgsOf(updated)

	// Reconcile for the same reason every other mutation does: saved state and
	// running state are never allowed to drift. The arguments ride in the
	// destination's restart signature, so this is what actually applies them —
	// the destination is torn down and respawned with the new command line.
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after expert args update", "err", err)
	}

	// Resolved against the row that was written, so the command shown back is
	// the one the reconcile above just started.
	cmd, cerr := s.resolveExpertCommand(updated, in, out)
	if cerr != nil {
		writeExpertCommandError(w, cerr)
		return
	}
	writeJSON(w, http.StatusOK, expertResponse{
		DestinationID: id,
		Args:          saved,
		Enabled:       saved.set(),
		Command:       cmd,
		Guards:        guards,
		Passthrough:   updated.RenditionID == nil,
		Applied:       true,
	})
}

// handleDeleteExpert drops back to the generated command.
func (s *Server) handleDeleteExpert(w http.ResponseWriter, r *http.Request) {
	id, _, ok := s.expertDestination(w, r)
	if !ok {
		return
	}
	// The acknowledgement goes with them: there is no longer an override to
	// have agreed to, and leaving it set would silently pre-approve the next one.
	cleared, err := s.saveExpertArgs(id, expertArgs{})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.reconcile(); err != nil {
		s.log.Warn("reconcile after expert args delete", "err", err)
	}
	resp := expertResponse{
		DestinationID: id,
		Passthrough:   cleared.RenditionID == nil,
		Applied:       true,
	}
	if cmd, err := s.resolveExpertCommand(cleared, nil, nil); err == nil {
		resp.Command = cmd
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleDryRunExpert puts the resolved command in front of FFmpeg with the
// output discarded, so a misspelt flag is caught here rather than by a
// destination that crash-loops after the stream is live.
func (s *Server) handleDryRunExpert(w http.ResponseWriter, r *http.Request) {
	_, row, ok := s.expertDestination(w, r)
	if !ok {
		return
	}
	var req expertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in, out, _, err := parseExpertRequest(req, row)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Guards are deliberately not enforced here. A dry run is how an operator
	// finds out whether the thing they are considering even parses; refusing to
	// tell them until they have acknowledged it would be backwards.
	cmd, err := s.resolveExpertCommand(row, in, out)
	if err != nil {
		writeExpertCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runDryRun(r.Context(), cmd.Bin, cmd.Argv))
}

// guardRefusal renders every guard into one message, because a client that
// sends two overrides should learn about both at once.
func guardRefusal(guards []expertGuard) string {
	reasons := make([]string, 0, len(guards))
	for _, g := range guards {
		reasons = append(reasons, g.Reason)
	}
	return strings.Join(reasons, " ") +
		" Repeat the request with \"ackReencode\": true to accept this."
}

// expertDestination resolves the {id} path parameter to a destination, writing
// the error response itself. Every handler here starts with it.
func (s *Server) expertDestination(w http.ResponseWriter, r *http.Request) (int64, *db.Destination, bool) {
	id, err := idParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, nil, false
	}
	row, err := s.store.GetDestination(id)
	if err != nil {
		writeStoreError(w, err)
		return 0, nil, false
	}
	return id, row, true
}
