package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/auth"
	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/chat"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/oauth"
	"github.com/rainmanjam/polyemesis/internal/overlay"
	"github.com/rainmanjam/polyemesis/internal/platform"
	"github.com/rainmanjam/polyemesis/internal/polls"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/scheduler"
)

// GO IS THE SOURCE FOR EVERY VALUE ANOTHER LANGUAGE COPIES.
//
// The console (ui/), the Stream Deck plugin and the site each restated words
// and numbers the server owns: the failure causes, the state words, the event
// types, the routing bounds, the poll and overlay limits, the platform names. Each copy was
// either unguarded or guarded by a test that compared it to a literal typed in
// the test, so a server change passed. This test builds one description from the
// Go values themselves and writes it where each client reads it; the clients
// derive from the file (the console's types are `typeof` the generated arrays)
// or test against it, so none of them can move on its own.
//
// What it does NOT cover: the bounds in ui/src/lib/limits.ts (ports, SRT latency,
// audio bitrate, recording segment, chat, quota, alert, analytics), which
// internal/db's TestUIBoundsMatchTheGoValidators holds to the Go validators.
//
// Regenerate with: go test ./internal/api/ -run TestWireContract -update-wire-contract
var updateWireContract = flag.Bool("update-wire-contract", false,
	"rewrite the generated wire contract files from the server's own values")

// Where each client reads the contract.
const (
	wireConsoleFile    = "../../ui/src/lib/contract/wire.generated.ts"
	wireStreamDeckFile = "../../integrations/streamdeck/test/contract/wire.json"
	wireSiteFile       = "../../web/src/data/facts.json"
	wireFiguresFile    = "../../web/src/data/preset-figures.json"
)

type wireRouting struct {
	MaxTracks              int     `json:"maxTracks"`
	MaxLabelLen            int     `json:"maxLabelLen"`
	MaxLangTagLen          int     `json:"maxLangTagLen"`
	MinDelayMs             int     `json:"minDelayMs"`
	MaxDelayMs             int     `json:"maxDelayMs"`
	MinTargetLufs          float64 `json:"minTargetLufs"`
	MaxTargetLufs          float64 `json:"maxTargetLufs"`
	MinTruePeakDb          float64 `json:"minTruePeakDb"`
	MaxTruePeakDb          float64 `json:"maxTruePeakDb"`
	MinLoudnessLra         float64 `json:"minLoudnessLra"`
	MaxLoudnessLra         float64 `json:"maxLoudnessLra"`
	DefaultTruePeakDb      float64 `json:"defaultTruePeakDb"`
	DefaultLoudnessLra     float64 `json:"defaultLoudnessLra"`
	LufsStreaming          float64 `json:"lufsStreaming"`
	LufsPodcast            float64 `json:"lufsPodcast"`
	LufsBroadcast          float64 `json:"lufsBroadcast"`
	MinDuckThresholdDb     float64 `json:"minDuckThresholdDb"`
	MaxDuckThresholdDb     float64 `json:"maxDuckThresholdDb"`
	MinDuckRatio           float64 `json:"minDuckRatio"`
	MaxDuckRatio           float64 `json:"maxDuckRatio"`
	DefaultDuckThresholdDb float64 `json:"defaultDuckThresholdDb"`
	DefaultDuckRatio       float64 `json:"defaultDuckRatio"`
	DefaultDuckAttackMs    float64 `json:"defaultDuckAttackMs"`
	DefaultDuckReleaseMs   float64 `json:"defaultDuckReleaseMs"`
}

// wirePolls are internal/polls' limits on what a poll may be; the form checks
// them before it sends.
type wirePolls struct {
	MinOptions         int `json:"minOptions"`
	MaxOptions         int `json:"maxOptions"`
	MaxQuestion        int `json:"maxQuestion"`
	MaxOption          int `json:"maxOption"`
	MinDurationSeconds int `json:"minDurationSeconds"`
	MaxDurationSeconds int `json:"maxDurationSeconds"`
}

// wireTwitchTools are internal/chat's limits on the Twitch channel tools; the
// panel checks them before it sends.
type wireTwitchTools struct {
	MaxAnnouncement int `json:"maxAnnouncement"`
	MaxWarning      int `json:"maxWarning"`
	MinTerm         int `json:"minTerm"`
	MaxTerm         int `json:"maxTerm"`
	MinPinSeconds   int `json:"minPinSeconds"`
	MaxPinSeconds   int `json:"maxPinSeconds"`
}

type wireRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// wireOverlay is internal/overlay's closed font set and style bounds.
type wireOverlay struct {
	Fonts             []string  `json:"fonts"`
	FontSize          wireRange `json:"fontSize"`
	FadeSeconds       wireRange `json:"fadeSeconds"`
	MaxLines          wireRange `json:"maxLines"`
	BackgroundOpacity wireRange `json:"backgroundOpacity"`
}

// wireAuth is the double-submit CSRF pair: the cookie the server sets and the
// header the console must echo it in. A rename on one side alone would turn
// every mutation into a 403.
type wireAuth struct {
	CSRFCookie string `json:"csrfCookie"`
	CSRFHeader string `json:"csrfHeader"`
}

// wireCaptions is the one exception in db.(*Destination).OutputDelaySeconds: a
// destination on this platform with this caption mode keeps its delay but
// YouTube applies it, so polyemesis holds the output back by 0.
type wireCaptions struct {
	NoLocalDelayPlatform string `json:"noLocalDelayPlatform"`
	NoLocalDelayMode     string `json:"noLocalDelayMode"`
}

type wireLimits struct {
	MaxThumbnailBytes              int `json:"maxThumbnailBytes"`
	DeviceMinPollMs                int `json:"deviceMinPollMs"`
	FacebookStreamHealthIntervalMs int `json:"facebookStreamHealthIntervalMs"`
	YouTubeVerticalPollMs          int `json:"youTubeVerticalPollMs"`
	MaxDumpSeconds                 int `json:"maxDumpSeconds"`
	BulkStartPacingMs              int `json:"bulkStartPacingMs"`
}

// wireRenditionBounds is the shape GET /renditions/presets serves under
// "bounds", so the console's form has the same limits before that answers.
type wireRenditionBounds struct {
	MinDimension  int     `json:"minDimension"`
	MaxDimension  int     `json:"maxDimension"`
	MaxFps        int     `json:"maxFps"`
	MinBitrate    int     `json:"minBitrate"`
	MaxBitrate    int     `json:"maxBitrate"`
	MinGopSeconds float64 `json:"minGopSeconds"`
	MaxGopSeconds float64 `json:"maxGopSeconds"`
}

// wireDefaults are the values db.DefaultSettings gives, for the settings form's
// placeholders and for a response from a server older than a field. They are
// read from DefaultSettings, never typed here.
type wireDefaults struct {
	SRTPort                 int `json:"srtPort"`
	RTMPPort                int `json:"rtmpPort"`
	ChatRetentionHours      int `json:"chatRetentionHours"`
	ChatKeepMessages        int `json:"chatKeepMessages"`
	ChatPurgeMinutes        int `json:"chatPurgeMinutes"`
	ChatHistoryMessages     int `json:"chatHistoryMessages"`
	YouTubeQuotaUnits       int `json:"youTubeQuotaUnits"`
	YouTubeQuotaReserve     int `json:"youTubeQuotaReserve"`
	YouTubeQuotaReserveFrac int `json:"youTubeQuotaReserveFraction"`
	AlertRetryAttempts      int `json:"alertRetryAttempts"`
	FailoverGraceSeconds    int `json:"failoverGraceSeconds"`
	FailoverReturnStableSec int `json:"failoverReturnStableSeconds"`
	MQTTIntervalSeconds     int `json:"mqttIntervalSeconds"`
	MQTTKeepAliveSeconds    int `json:"mqttKeepAliveSeconds"`
}

type wireSite struct {
	Comment        string `json:"_comment"`
	MaxTracks      int    `json:"maxTracks"`
	FFmpegMinMajor int    `json:"ffmpegMinMajor"`
	GoMinVersion   string `json:"goMinVersion"`
	// UINodeEngines is the Node range the UI build declares: the floor "build
	// from a clone" asks of a reader.
	UINodeEngines string `json:"uiNodeEngines"`
}

type wireContract struct {
	Comment             string   `json:"_comment"`
	FailureCauses       []string `json:"failureCauses"`
	ProcessStates       []string `json:"processStates"`
	JobStates           []string `json:"jobStates"`
	TokenScopes         []string `json:"tokenScopes"`
	ChatStates          []string `json:"chatStates"`
	ObsConnStates       []string `json:"obsConnStates"`
	OffloadStates       []string `json:"offloadStates"`
	EventTypes          []string `json:"eventTypes"`
	SourceKinds         []string `json:"sourceKinds"`
	RecordingStates     []string `json:"recordingStates"`
	AccountHealthStates []string `json:"accountHealthStates"`
	// AppliedStates is how a save took effect ("live" or "restart"); the
	// console and the Stream Deck plugin both read it.
	AppliedStates   []string            `json:"appliedStates"`
	ViewerPlatforms []string            `json:"viewerPlatforms"`
	Routing         wireRouting         `json:"routing"`
	Limits          wireLimits          `json:"limits"`
	Polls           wirePolls           `json:"polls"`
	TwitchTools     wireTwitchTools     `json:"twitchTools"`
	Overlay         wireOverlay         `json:"overlay"`
	Auth            wireAuth            `json:"auth"`
	Captions        wireCaptions        `json:"captions"`
	Defaults        wireDefaults        `json:"defaults"`
	RenditionBounds wireRenditionBounds `json:"renditionBounds"`
	Platforms       platform.Directory  `json:"platforms"`
}

const wireComment = "Generated by internal/api's TestWireContract. Do not edit by hand: " +
	"regenerate with `go test ./internal/api/ -run TestWireContract -update-wire-contract`."

// repoFile reads one repository file by its path from internal/api.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// stringConsts returns the string constants of one package directory that
// satisfy keep, in declaration order (files sorted by name). Reading the const
// block rather than listing the names here is what makes a new state word
// reach the contract without anybody remembering to add it to a list.
func stringConsts(t *testing.T, dir string, keep func(typeName, constName string) bool) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	sort.Strings(files)
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				typeName := ""
				if id, ok := vs.Type.(*ast.Ident); ok {
					typeName = id.Name
				}
				for i, n := range vs.Names {
					if i >= len(vs.Values) || !keep(typeName, n.Name) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", n.Name, err)
					}
					out = append(out, v)
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no constants found in %s: the filter matches nothing, so the contract would silently be empty", dir)
	}
	return out
}

// mapKeys returns the keys of the package-level `var <name> = map[string]...{}`
// in one package directory, in source order. A var cannot be read through the
// package's API when it is unexported, and listing the keys here would be the
// copy this file exists to remove.
func mapKeys(t *testing.T, dir, name string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				if len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
					continue
				}
				cl, ok := vs.Values[0].(*ast.CompositeLit)
				if !ok {
					continue
				}
				var keys []string
				for _, el := range cl.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					lit, ok := kv.Key.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s in %s has a key that is not a string literal: the contract would silently omit it", name, dir)
					}
					k, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					keys = append(keys, k)
				}
				if len(keys) == 0 {
					t.Fatalf("%s in %s has no string keys", name, dir)
				}
				return keys
			}
		}
	}
	t.Fatalf("no `var %s = map...` in %s", name, dir)
	return nil
}

// overlayOpacityRange is the one overlay bound with no constant behind it
// (Style.Validate spells 0 and 100 inline), so it is read from the validator's
// behaviour: the contract carries the edge that validator accepts, and fails
// loudly if either edge or the step outside it moves.
func overlayOpacityRange(t *testing.T) wireRange {
	t.Helper()
	accepts := func(v int) bool {
		s := overlay.DefaultStyle()
		s.BackgroundOpacity = v
		return s.Validate() == nil
	}
	r := wireRange{Min: 0, Max: 100}
	if !accepts(r.Min) || accepts(r.Min-1) || !accepts(r.Max) || accepts(r.Max+1) {
		t.Fatalf("overlay.Style.Validate no longer accepts exactly %d..%d for backgroundOpacity", r.Min, r.Max)
	}
	return r
}

// overlayFadeMin is the same, for fadeSeconds' floor.
func overlayFadeMin(t *testing.T) int {
	t.Helper()
	accepts := func(v int) bool {
		s := overlay.DefaultStyle()
		s.FadeSeconds = v
		return s.Validate() == nil
	}
	if !accepts(0) || accepts(-1) {
		t.Fatal("overlay.Style.Validate no longer accepts fadeSeconds from exactly 0")
	}
	return 0
}

func ofType(want string) func(string, string) bool {
	return func(typ, _ string) bool { return typ == want }
}

func withPrefix(prefix string, not ...string) func(string, string) bool {
	return func(_, name string) bool {
		if !strings.HasPrefix(name, prefix) {
			return false
		}
		for _, n := range not {
			if strings.HasPrefix(name, n) {
				return false
			}
		}
		return true
	}
}

func causeStrings() []string {
	var out []string
	for _, c := range failreason.AllCauses() {
		out = append(out, string(c))
	}
	return out
}

func jobStateStrings() []string {
	var out []string
	for _, s := range jobs.AllStates() {
		out = append(out, string(s))
	}
	return out
}

func eventTypeStrings() []string {
	var out []string
	for _, e := range events.AllTypes() {
		out = append(out, string(e))
	}
	return out
}

// viewerPlatforms are the platforms a viewers key may show alone: every
// platform whose capability row says polyemesis reads viewer counts, and the
// install's own player.
func viewerPlatforms() []string {
	var out []string
	for _, row := range oauth.PlatformCapabilities() {
		if row.Platform != "" && row.Get(oauth.CapViewerStats) == oauth.SupportYes {
			out = append(out, string(row.Platform))
		}
	}
	return append(out, analytics.PlatformPlayer)
}

func goModVersion(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+(?:\.\d+)?)$`).FindStringSubmatch(repoFile(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	return m[1]
}

func buildWireContract(t *testing.T) wireContract {
	t.Helper()
	def := db.DefaultSettings()
	ms := func(d time.Duration) int { return int(d / time.Millisecond) }
	return wireContract{
		Comment:       wireComment,
		FailureCauses: causeStrings(),
		ProcessStates: stringConsts(t, "internal/supervisor", ofType("State")),
		JobStates:     jobStateStrings(),
		TokenScopes:   stringConsts(t, "internal/db", withPrefix("Scope")),
		ChatStates:    stringConsts(t, "internal/chat", ofType("State")),
		ObsConnStates: stringConsts(t, "internal/obsbridge", ofType("ConnState")),
		OffloadStates: stringConsts(t, "internal/db", withPrefix("Offload", "OffloadKind")),
		EventTypes:    eventTypeStrings(),
		SourceKinds:   stringConsts(t, "internal/engine", ofType("sourceKind")),
		RecordingStates: []string{
			recordingStateOff, recordingStateRecording, recordingStateNotRunning, recordingStateHaltedDisk,
		},
		AccountHealthStates: []string{
			AccountHealthOK, AccountHealthExpiring, AccountHealthRefreshFailing, AccountHealthExpired,
		},
		AppliedStates:   []string{engine.AppliedLive, engine.AppliedRestart},
		ViewerPlatforms: viewerPlatforms(),
		Routing: wireRouting{
			MaxTracks: routing.MaxTracks, MaxLabelLen: routing.MaxLabelLen, MaxLangTagLen: routing.MaxLangTagLen,
			MinDelayMs: routing.MinDelayMS, MaxDelayMs: routing.MaxDelayMS,
			MinTargetLufs: routing.MinTargetLUFS, MaxTargetLufs: routing.MaxTargetLUFS,
			MinTruePeakDb: routing.MinTruePeakDB, MaxTruePeakDb: routing.MaxTruePeakDB,
			MinLoudnessLra: routing.MinLoudnessLRA, MaxLoudnessLra: routing.MaxLoudnessLRA,
			DefaultTruePeakDb: routing.DefaultTruePeakDB, DefaultLoudnessLra: routing.DefaultLoudnessLRA,
			LufsStreaming: routing.LUFSStreaming, LufsPodcast: routing.LUFSPodcast, LufsBroadcast: routing.LUFSBroadcast,
			MinDuckThresholdDb: routing.MinDuckThresholdDB, MaxDuckThresholdDb: routing.MaxDuckThresholdDB,
			MinDuckRatio: routing.MinDuckRatio, MaxDuckRatio: routing.MaxDuckRatio,
			DefaultDuckThresholdDb: routing.DefaultDuckThresholdDB, DefaultDuckRatio: routing.DefaultDuckRatio,
			DefaultDuckAttackMs: routing.DefaultDuckAttackMS, DefaultDuckReleaseMs: routing.DefaultDuckReleaseMS,
		},
		Limits: wireLimits{
			MaxThumbnailBytes:              scheduler.MaxThumbnailBytes,
			DeviceMinPollMs:                ms(oauth.DeviceMinPollInterval),
			FacebookStreamHealthIntervalMs: ms(oauth.FacebookStreamHealthInterval),
			YouTubeVerticalPollMs:          ms(verticalHealthMinInterval),
			MaxDumpSeconds:                 int(engine.MaxDumpRequest.Seconds()),
			BulkStartPacingMs:              ms(bulkStartPacing),
		},
		Polls: wirePolls{
			MinOptions: polls.MinOptions, MaxOptions: polls.MaxOptions,
			MaxQuestion: polls.MaxQuestion, MaxOption: polls.MaxOption,
			MinDurationSeconds: int(polls.MinDuration.Seconds()), MaxDurationSeconds: int(polls.MaxDuration.Seconds()),
		},
		TwitchTools: wireTwitchTools{
			MaxAnnouncement: chat.TwitchMaxAnnouncement, MaxWarning: chat.TwitchMaxWarning,
			MinTerm: chat.TwitchMinTerm, MaxTerm: chat.TwitchMaxTerm,
			MinPinSeconds: chat.TwitchMinPinSeconds, MaxPinSeconds: chat.TwitchMaxPinSeconds,
		},
		Overlay: wireOverlay{
			Fonts:             mapKeys(t, "internal/overlay", "fonts"),
			FontSize:          wireRange{Min: overlay.MinFontSize, Max: overlay.MaxFontSize},
			FadeSeconds:       wireRange{Min: overlayFadeMin(t), Max: overlay.MaxFade},
			MaxLines:          wireRange{Min: overlay.MinLines, Max: overlay.MaxLines},
			BackgroundOpacity: overlayOpacityRange(t),
		},
		Auth: wireAuth{CSRFCookie: auth.CSRFCookie, CSRFHeader: auth.CSRFHeader},
		Captions: wireCaptions{
			NoLocalDelayPlatform: string(db.PlatformYouTube),
			NoLocalDelayMode:     string(captions.ModeYouTubePOST),
		},
		Defaults: wireDefaults{
			SRTPort: def.Listeners.SRTPort, RTMPPort: def.Listeners.RTMPPort,
			ChatRetentionHours: def.Chat.RetentionHours, ChatKeepMessages: def.Chat.KeepMessages,
			ChatPurgeMinutes: def.Chat.PurgeMinutes, ChatHistoryMessages: def.Chat.HistoryMessages,
			YouTubeQuotaUnits: def.Chat.YouTubeQuotaUnits, YouTubeQuotaReserve: def.Chat.YouTubeQuotaReserve,
			YouTubeQuotaReserveFrac: db.MaxYouTubeQuotaReserveFraction,
			AlertRetryAttempts:      def.Alerts.RetryAttempts,
			FailoverGraceSeconds:    def.Failover.GraceSeconds, FailoverReturnStableSec: def.Failover.ReturnStableSeconds,
			MQTTIntervalSeconds: def.MQTT.IntervalSecond, MQTTKeepAliveSeconds: def.MQTT.KeepAliveSec,
		},
		RenditionBounds: wireRenditionBounds{
			MinDimension: db.MinRenditionDimension, MaxDimension: db.MaxRenditionDimension,
			MaxFps: db.MaxRenditionFPS, MinBitrate: db.MinRenditionBitrate, MaxBitrate: db.MaxRenditionBitrate,
			MinGopSeconds: db.MinRenditionGOP, MaxGopSeconds: db.MaxRenditionGOP,
		},
		Platforms: platform.Describe(),
	}
}

func marshalWire(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return buf.Bytes()
}

// consoleSource is the generated TypeScript module: the same JSON, as a const
// literal, so `typeof WIRE.failureCauses[number]` is a union the console can use
// as a type.
func consoleSource(t *testing.T, c wireContract) []byte {
	t.Helper()
	body := marshalWire(t, c)
	return []byte("// " + wireComment + "\n" +
		"// The console's contract with the server: see ui/src/lib/contract/wire.ts.\n\n" +
		"export const WIRE = " + strings.TrimRight(string(body), "\n") + " as const;\n")
}

func uiNodeEngines(t *testing.T) string {
	t.Helper()
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal([]byte(repoFile(t, "ui/package.json")), &pkg); err != nil || pkg.Engines.Node == "" {
		t.Fatalf("ui/package.json has no engines.node: %v", err)
	}
	return pkg.Engines.Node
}

func siteFacts(t *testing.T) wireSite {
	t.Helper()
	return wireSite{
		UINodeEngines:  uiNodeEngines(t),
		Comment:        wireComment,
		MaxTracks:      routing.MaxTracks,
		FFmpegMinMajor: ffmpeg.MinMajorVersion,
		GoMinVersion:   goModVersion(t),
	}
}

// presetFigure is one catalogue preset as the site's calculator transcribes it.
type presetFigure struct {
	ID         string `json:"id"`
	Integrated bool   `json:"integrated"`
	Transport  string `json:"transport"`
	KbpsMin    int    `json:"kbpsMin,omitempty"`
	KbpsMax    int    `json:"kbpsMax,omitempty"`
	Checked    string `json:"checked,omitempty"`
}

// presetFigures is what web/scripts/check-build.mjs compares the calculator and
// the "N other presets" sentence with. It used to read internal/db/platforms.go
// with a regexp, which stopped finding anything when the guidance moved into the
// platform registry; it reads the catalogue's own values now.
func presetFigures() []presetFigure {
	var out []presetFigure
	for _, p := range db.DestinationPresets() {
		f := presetFigure{ID: p.ID, Integrated: p.Platform != "", Transport: string(p.Transport)}
		if p.Video != nil {
			f.KbpsMin, f.KbpsMax, f.Checked = p.Video.KbpsMin, p.Video.KbpsMax, p.Video.Checked
		}
		out = append(out, f)
	}
	return out
}

// TestWireContract holds the three generated files to what the server says.
func TestWireContract(t *testing.T) {
	c := buildWireContract(t)
	outputs := map[string][]byte{
		wireConsoleFile:    consoleSource(t, c),
		wireStreamDeckFile: marshalWire(t, c),
		wireSiteFile:       marshalWire(t, siteFacts(t)),
		wireFiguresFile:    marshalWire(t, map[string]any{"_comment": wireComment, "presets": presetFigures()}),
	}
	for path, want := range outputs {
		if *updateWireContract {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("cannot read %s: %v\nRegenerate with: go test ./internal/api/ -run TestWireContract -update-wire-contract", path, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s is out of date: the server's values and the committed contract disagree.\n"+
				"Regenerate with: go test ./internal/api/ -run TestWireContract -update-wire-contract", path)
		}
	}
}

// The registry endpoint serves what the console was built against.
//
// The console reads the registry from the generated file and a scripted client
// reads it from GET /platforms/registry. They are one description, so a field
// added to Info reaches both or neither.
func TestThePlatformRegistryEndpointServesTheContract(t *testing.T) {
	_, h, _ := testServer(t, config.Config{})
	sign := login(t, h)
	req := jsonRequest(t, http.MethodGet, "/api/v1/platforms/registry", nil)
	sign(req)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var served, built any
	if err := json.Unmarshal(rr.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(buildWireContract(t).Platforms)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &built); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(served, built) {
		t.Errorf("GET /platforms/registry and the generated contract disagree:\nserved %s\nbuilt  %s", rr.Body, b)
	}
	// Row has no JSON tags and carries list-order fields; the endpoint must not
	// serve its shape.
	for _, internal := range []string{`"Order"`, `"SendMax"`, `"Guidance"`, `"Ceilings"`} {
		if strings.Contains(rr.Body.String(), internal) {
			t.Errorf("the registry endpoint exposes the internal field %s", internal)
		}
	}
}

// The captions block says what OutputDelaySeconds does: the contract's one
// exception holds 0, and every other platform/mode pair holds the delay.
func TestWireCaptionsRuleIsTheDestinationRule(t *testing.T) {
	c := buildWireContract(t).Captions
	exempt := &db.Destination{Platform: db.Platform(c.NoLocalDelayPlatform), CaptionMode: c.NoLocalDelayMode, DelaySeconds: 30}
	if got := exempt.OutputDelaySeconds(); got != 0 {
		t.Errorf("%s with %s holds %d s: the contract says it holds none", c.NoLocalDelayPlatform, c.NoLocalDelayMode, got)
	}
	for _, d := range []*db.Destination{
		{Platform: db.Platform(c.NoLocalDelayPlatform), CaptionMode: string(captions.ModeOff), DelaySeconds: 30},
		{Platform: db.PlatformTwitch, CaptionMode: c.NoLocalDelayMode, DelaySeconds: 30},
	} {
		if got := d.OutputDelaySeconds(); got != 30 {
			t.Errorf("%s with %s holds %d s: only the contract's pair is exempt", d.Platform, d.CaptionMode, got)
		}
	}
}
