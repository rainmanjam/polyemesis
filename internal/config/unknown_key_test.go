package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A MISSPELLED KEY ANYWHERE IN config.yaml USED TO BE IGNORED, SILENTLY.
//
// Load was yaml.Unmarshal, and an unrecognised key was dropped on the floor, so
// every typo became a default nobody chose: `trustProxyhHeaders: true` left the
// session cookie without its Secure flag behind a proxy, `tsl:` left TLS off,
// `DataDir:` put the database in ./data. The tls block was tightened first
// (f1b0da38); this is the rest of the file, nested blocks included.
//
// Mutation: in decodeStrict, drop dec.KnownFields(true). Observed to fail with
// every case below loading without error.
func TestAnUnknownKeyAnywhereRefusesToLoad(t *testing.T) {
	const top = "Valid top-level keys: addr, dataDir, tls, ffmpeg, transcription, trustProxyHeaders"
	for _, tc := range []struct {
		name, body, wantKey, wantHint, wantList string
	}{
		{"misspelled top level", "trustProxyhHeaders: true\n", "trustProxyhHeaders", "", top},
		{"misspelled block", "tsl:\n  mode: selfsigned\n", "tsl", "", top},
		{"wrong case", "DataDir: /srv/polyemesis\n", "DataDir", "dataDir", top},
		{"inside ffmpeg", "ffmpeg:\n  binery: /opt/ffmpeg\n", "binery", "", "Valid keys under ffmpeg: binary, probe"},
		{"wrong case inside ffmpeg", "ffmpeg:\n  Binary: /opt/ffmpeg\n", "Binary", "binary", "Valid keys under ffmpeg: binary, probe"},
		{"inside transcription", "transcription:\n  bin: /opt/whisper\n", "bin", "", "Valid keys under transcription: binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("loaded (addr %q, dataDir %q): a misspelled key was silently ignored",
					cfg.Addr, cfg.DataDir)
			}
			msg := err.Error()
			if !strings.Contains(msg, `"`+tc.wantKey+`"`) {
				t.Errorf("error %q does not name the key %q", msg, tc.wantKey)
			}
			if !strings.Contains(msg, "Refusing to start") {
				t.Errorf("error %q does not say why it stopped", msg)
			}
			if tc.wantHint != "" && !strings.Contains(msg, "did you mean \""+tc.wantHint+"\"") {
				t.Errorf("error %q does not suggest %q", msg, tc.wantHint)
			}
			// The keys listed are the ones valid WHERE THE TYPO IS: the
			// top-level list under ffmpeg: sends the operator to the wrong place.
			if !strings.Contains(msg, tc.wantList) {
				t.Errorf("error %q does not list %q", msg, tc.wantList)
			}
			// The Go types yaml.v3 decoded into are not the operator's words.
			if strings.Contains(msg, "in type") || strings.Contains(msg, "config.onDisk") ||
				strings.Contains(msg, "config.FFmpeg") || strings.Contains(msg, "config.Transcription") {
				t.Errorf("error %q leaks an internal type name", msg)
			}
		})
	}
}

// Several typos are reported together, each against its own block, so the
// operator fixes the file in one pass rather than one restart per key.
func TestEveryUnknownKeyIsNamedAgainstItsOwnBlock(t *testing.T) {
	_, err := Load(writeConfig(t, "ffmpeg:\n  Probe: /opt/ffprobe\nAddr: 127.0.0.1:9000\n"))
	if err == nil {
		t.Fatal("two misspelled keys loaded")
	}
	for _, want := range []string{
		`line 2: unknown key "Probe" under ffmpeg (did you mean "probe"?`,
		`line 3: unknown key "Addr" at the top level (did you mean "addr"?`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A block explainUnknownKey has no map entry for still names the key and the
// line; it gives no list rather than one from the wrong level. Lines that are
// not unknown keys ride along untouched.
func TestAnUnknownKeyInAnUnmappedBlockGetsNoBorrowedList(t *testing.T) {
	err := explainUnknownKey(&yaml.TypeError{Errors: []string{
		"line 4: field colour not found in type config.somethingNew",
		"line 5: cannot unmarshal !!seq into string",
	}})
	msg := err.Error()
	for _, want := range []string{`line 4: unknown key "colour"`, "line 5: cannot unmarshal", "Refusing to start"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "Valid") {
		t.Errorf("error %q lists keys from a block it could not identify", msg)
	}
}

// Strictness must not turn a file that used to mean "nothing set" into an
// error: yaml.v3's Decoder reports an empty document as io.EOF, where
// yaml.Unmarshal returned nil.
func TestAnEmptyOrCommentOnlyConfigStillLoadsTheDefaults(t *testing.T) {
	for _, body := range []string{"", "# nothing configured yet\n", "\n\n"} {
		cfg, err := Load(writeConfig(t, body))
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if cfg.Addr != DefaultAddr || !cfg.AddrDefaulted || cfg.TLS.Mode != ModeOff {
			t.Errorf("Load(%q) = addr %q defaulted %v mode %q; want the defaults",
				body, cfg.Addr, cfg.AddrDefaulted, cfg.TLS.Mode)
		}
	}
}

// Every other decode error still arrives as it came, not dressed up as an
// unknown key.
func TestADecodeErrorThatIsNotAnUnknownKeyIsReportedAsItIs(t *testing.T) {
	_, err := Load(writeConfig(t, "addr: [1, 2]\n"))
	if err == nil {
		t.Fatal("a list where addr's string belongs loaded")
	}
	if strings.Contains(err.Error(), "Refusing to start") {
		t.Errorf("a type error was reported as an unknown key: %v", err)
	}
}

// A HOSTNAME WITH NO MODE IS A CERTIFICATE REQUEST THAT SERVES PLAINTEXT.
//
// `tls: {hostname: staging.example}` -- mode forgotten, or deleted while the
// block was being edited -- maps to off, and the server came up on plain HTTP
// while the operator believed they had configured HTTPS for that name.
//
// Mutation: make checkHostnameWithoutMode return nil. Observed to fail with the
// refusal cases loading as mode "off".
func TestAHostnameWithoutAModeRefusesToLoad(t *testing.T) {
	for _, body := range []string{
		"tls:\n  hostname: staging.example.com\n",
		"tls:\n  mode: \"\"\n  hostname: staging.example.com\n",
		"tls:\n  enabled: false\n  hostname: box.lan\n",
	} {
		cfg, err := Load(writeConfig(t, body))
		if err == nil {
			t.Fatalf("Load(%q) started as mode %q: a hostname with no mode served plaintext", body, cfg.TLS.Mode)
		}
		if !strings.Contains(err.Error(), "tls.mode is not set") {
			t.Errorf("Load(%q): error %q does not name the missing mode", body, err)
		}
	}

	// Each of these said something on purpose and must keep loading.
	cert, key := certPair(t)
	for _, body := range []string{
		"tls:\n  mode: \"off\"\n  hostname: stream.example.com\n",
		"trustProxyHeaders: true\ntls:\n  mode: auto\n  hostname: stream.example.com\n",
		"tls:\n  enabled: true\n  hostname: box.lan\n  certFile: " + cert + "\n  keyFile: " + key + "\n",
		"tls:\n  enabled: false\n",
	} {
		if _, err := Load(writeConfig(t, body)); err != nil {
			t.Errorf("Load(%q): %v", body, err)
		}
	}
}
