package config

import (
	"strings"
	"testing"
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
	for _, tc := range []struct {
		name, body, wantKey, wantHint string
	}{
		{"misspelled top level", "trustProxyhHeaders: true\n", "trustProxyhHeaders", ""},
		{"misspelled block", "tsl:\n  mode: selfsigned\n", "tsl", ""},
		{"wrong case", "DataDir: /srv/polyemesis\n", "DataDir", "dataDir"},
		{"inside ffmpeg", "ffmpeg:\n  binery: /opt/ffmpeg\n", "binery", ""},
		{"inside transcription", "transcription:\n  bin: /opt/whisper\n", "bin", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatalf("loaded (addr %q, dataDir %q): a misspelled key was silently ignored",
					cfg.Addr, cfg.DataDir)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q does not name the key %q", err, tc.wantKey)
			}
			if !strings.Contains(err.Error(), "Refusing to start") {
				t.Errorf("error %q does not say why it stopped", err)
			}
			if tc.wantHint != "" && !strings.Contains(err.Error(), `"`+tc.wantHint+`"`) {
				t.Errorf("error %q does not suggest %q", err, tc.wantHint)
			}
		})
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
