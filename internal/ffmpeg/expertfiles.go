package ffmpeg

import (
	"fmt"
	"regexp"
	"strings"
)

// filterGraphFileNote is why each flag that reads a filter graph from disk is refused.
const filterGraphFileNote = "reads a filter graph from a file on the server, past every check on this page"

// The expert-argument file-access check lives here, beside SplitArgs, so the
// API (on the way in) and the engine (at every spawn) run the same code: a row
// stored before a rule existed is dropped at spawn exactly as the editor would
// refuse it now.

// ------------------------------------------------- file access, refused
//
// AN FFMPEG ARGV CAN READ AND WRITE FILES ON THIS BOX, and expert mode's
// acknowledgement does not make that safe: the operator confirms a stream
// setting, not "overwrite polyemesis.db". Poka-yoke audit 2026-11-04 showed
// `-progress /var/lib/polyemesis/polyemesis.db` (overwrite), a second
// positional output (a new file anywhere the service can write), and
// movie=/textfile= filters (read a server file out to the platform). So the
// options, muxers and filters that open a path the operator names are refused
// by construction, with nothing to acknowledge them away. Everything here is a
// refusal; none of it changes what reaches the platform in a way an operator
// streaming to it would want.

// fileOptions are FFmpeg options whose value (or, for the booleans, whose mere
// presence) opens a file on the server. Matched on the option's base name, so
// a stream specifier (-dump_attachment:t, -stats_enc_pre:v) does not hide one.
var fileOptions = map[string]string{
	"-progress":               "writes progress reports to the file or URL it names",
	"-vstats":                 "writes video statistics to a file in the server's working directory",
	"-vstats_file":            "writes video statistics to the file it names",
	"-pass":                   "writes a two-pass log file on the server",
	"-passlogfile":            "writes two-pass statistics to the file it names",
	"-dump_attachment":        "extracts attachments into files on the server",
	"-attach":                 "reads a file from the server into the output",
	"-sdp_file":               "writes an SDP description to the file it names",
	"-print_graphs_file":      "writes the execution graph to the file it names",
	"-stats_enc_pre":          "writes encoder statistics to the file it names",
	"-stats_enc_post":         "writes encoder statistics to the file it names",
	"-stats_mux_pre":          "writes muxer statistics to the file it names",
	"-fpre":                   "reads options from a preset file on the server, past every check on this page",
	"-filter_script":          filterGraphFileNote,
	"-filter_complex_script":  filterGraphFileNote,
	"-hls_segment_filename":   "writes HLS segments to the path it names",
	"-hls_fmp4_init_filename": "writes an HLS init segment to the path it names",
	"-hls_key_info_file":      "reads a key file from the server",
	"-segment_list":           "writes a segment list to the file it names",
	"-master_pl_name":         "writes a master playlist file on the server",
	"-x265-stats":             "writes x265's two-pass statistics to the file it names",
	"-graph_file":             filterGraphFileNote,
	"-dumpgraph":              "dumps the filter graph out of the process",
}

// outputFormats are the only values -f may take in the output arguments: the
// containers a destination streams. AN ALLOWLIST (review follow-up): the old
// list of file-writing muxers missed hds, fifo (which wraps any muxer),
// and every libavdevice output (fbdev, v4l2, alsa, ...), each of which opens a
// path or a device on the server.
var outputFormats = map[string]bool{
	"flv": true, "mpegts": true, "matroska": true, "webm": true, "mp4": true, "mov": true,
	"nut": true, "rtp": true, "rtp_mpegts": true, "rtsp": true, "ogg": true, "adts": true, "null": true,
	// The audio destinations' own muxers (audioFormats), so restating one is
	// never refused. TestEveryFormatADestinationEmitsIsAllowed holds this.
	"mp3": true, "ipod": true, "flac": true, "wav": true,
}

// inputFormats are the only values -f may take in the input arguments. The
// input is the relay's MPEG-TS; any other demuxer reinterprets its URL --
// -f lavfi as a filter graph, -f concat as a file list, a capture device.
var inputFormats = map[string]bool{"mpegts": true}

// filterGraphFlags carry a filter graph as their value.
var filterGraphFlags = map[string]bool{
	"-vf": true, "-af": true, "-filter": true, "-filter_complex": true, "-lavfi": true,
}

// allowedFilters is every filter an expert-mode graph may use. AN ALLOWLIST,
// NOT A DENYLIST: the first version listed file-opening filters and keys, and a
// security review found the gaps that shape always has -- psnr's positional
// stats_file, ametadata's file=, removelogo, sofalizer, curves' psfile, and
// whichever filter is added next. A filter is here only after its option table
// (`ffmpeg -h filter=<name>`, FFmpeg 9.0.2) was read and no option of it names
// a path, a library, a model or a font. loudnorm (stats_file), deshake
// (filename) and curves (psfile, plot) were read and left out for that reason;
// drawtext (textfile, fontfile) and paletteuse (debug_kdtree) likewise. A
// filter the operator needs and this
// list lacks is refused with its name, which is the prompt to vet and add it.
var allowedFilters = map[string]bool{
	// video geometry, timing and format
	"scale": true, "scale2ref": true, "fps": true, "format": true, "pad": true, "crop": true,
	"setsar": true, "setdar": true, "setpts": true, "settb": true, "framerate": true,
	"hflip": true, "vflip": true, "transpose": true, "rotate": true, "yadif": true, "bwdif": true,
	"tpad": true, "trim": true, "select": true, "minterpolate": true, "shuffleframes": true,
	"hwupload": true, "hwdownload": true, "hwmap": true,
	// video look
	"overlay": true, "drawbox": true, "drawgrid": true, "hqdn3d": true, "unsharp": true, "eq": true,
	"colorchannelmixer": true, "hue": true, "gblur": true, "boxblur": true, "colorspace": true,
	"tonemap": true, "colorlevels": true, "lut": true, "lutrgb": true, "lutyuv": true, "geq": true,
	"chromakey": true, "colorkey": true, "alphamerge": true, "alphaextract": true, "xfade": true,
	"fade": true, "palettegen": true,
	// audio
	"aresample": true, "volume": true, "anull": true, "amix": true, "amerge": true, "pan": true,
	"aformat": true, "channelsplit": true, "channelmap": true, "join": true, "adelay": true,
	"atempo": true, "dynaudnorm": true, "acompressor": true, "alimiter": true, "agate": true,
	"highpass": true, "lowpass": true, "bandpass": true, "bandreject": true, "equalizer": true,
	"bass": true, "treble": true, "afade": true, "acrossfade": true, "asetpts": true,
	"asettb": true, "atrim": true, "apad": true, "aselect": true, "aecho": true, "compand": true,
	"silenceremove": true, "sidechaincompress": true, "afftdn": true, "anlmdn": true,
	"stereotools": true, "extrastereo": true, "earwax": true,
	// audio to video
	"showwaves": true, "showspectrum": true, "showvolume": true, "avectorscope": true,
	// plumbing
	"null": true, "nullsink": true, "anullsink": true, "split": true, "asplit": true,
	"concat": true, "copy": true, "acopy": true,
}

// codecParamFlags carry an encoder's own key=value list, some of whose keys
// are paths (x264's dump-yuv writes raw video anywhere it is told to).
var codecParamFlags = map[string]bool{
	"-x264-params": true, "-x264opts": true, "-x265-params": true, "-svtav1-params": true,
	"-aom-params": true,
}

// codecParamKeys are the encoder keys expert mode accepts inside
// -x264-params, -x264opts, -x265-params, -svtav1-params and -aom-params: rate
// control, GOP, motion search, colour and tiling. AN ALLOWLIST (fourth security
// review): the encoders accept aliases and negated forms (no-stats, nostats),
// so no list of file-naming keys can be complete. Every key that names or
// writes a file -- stats, pass, dump-yuv, csv, qpfile, zonefile, analysis-*,
// recon, scaling-list, lambda-file, nalu-file, dhdr10-info, fgs-table,
// film-grain (a model file in x265) -- is absent, so it is refused in any
// spelling.
var codecParamKeys = map[string]bool{
	// rate control and GOP
	"keyint": true, "min-keyint": true, "keyint-min": true, "scenecut": true, "bframes": true,
	"b-adapt": true, "b-pyramid": true, "ref": true, "rc-lookahead": true, "lookahead": true,
	"vbv-maxrate": true, "vbv-bufsize": true, "vbv-init": true, "crf": true, "qp": true,
	"bitrate": true, "rc": true, "tbr": true, "mbr": true, "nal-hrd": true, "hrd": true,
	"force-cfr": true, "open-gop": true, "intra-refresh": true, "irefresh-type": true,
	"scd": true, "mbtree": true, "cutree": true, "filler": true, "min-qp": true, "max-qp": true,
	"qpmin": true, "qpmax": true, "qpstep": true, "ipratio": true, "pbratio": true,
	"buf-sz": true, "buf-initial-sz": true, "buf-optimal-sz": true, "undershoot-pct": true,
	"overshoot-pct": true, "lag-in-frames": true,
	// analysis and quality
	"aq-mode": true, "aq-strength": true, "psy-rd": true, "psy-rdoq": true, "psy": true,
	"me": true, "subme": true, "merange": true, "trellis": true, "weightp": true,
	"weightb": true, "8x8dct": true, "cabac": true, "deblock": true, "partitions": true,
	"direct": true, "mixed-refs": true, "fast-pskip": true, "dct-decimate": true, "rd": true,
	"ctu": true, "min-cu-size": true, "tu-intra-depth": true, "tu-inter-depth": true,
	"b-intra": true, "rect": true, "amp": true, "limit-refs": true, "sao": true,
	"strong-intra-smoothing": true, "preset": true, "tune": true, "profile": true,
	"level": true, "level-idc": true, "high-tier": true, "cpu-used": true,
	"enable-tf": true, "enable-qm": true, "qm-min": true, "qm-max": true, "enable-dlf": true,
	"enable-cdef": true, "enable-restoration": true, "sharpness": true,
	"variance-boost-strength": true, "enable-overlays": true, "fast-decode": true,
	"pred-struct": true, "hierarchical-levels": true, "enable-tpl-model": true,
	"deltaq-mode": true,
	// threading and slicing
	"threads": true, "sliced-threads": true, "sync-lookahead": true, "slices": true,
	"frame-threads": true, "pools": true, "wpp": true, "pmode": true, "pme": true,
	"lookahead-slices": true, "lp": true, "pin": true, "tile-rows": true,
	"tile-columns": true, "row-mt": true,
	// bitstream and colour
	"aud": true, "repeat-headers": true, "annexb": true, "bluray-compat": true, "info": true,
	"colorprim": true, "transfer": true, "colormatrix": true, "fullrange": true, "range": true,
	"color-primaries": true, "transfer-characteristics": true, "matrix-coefficients": true,
	"color-range": true, "hdr10": true, "hdr10-opt": true, "master-display": true,
	"max-cll": true, "mastering-display": true, "content-light": true,
}

// optionBase strips a stream specifier: -c:v:0 -> -c, -dump_attachment:t ->
// -dump_attachment.
func optionBase(flag string) string {
	if i := strings.IndexByte(flag, ':'); i > 0 {
		return flag[:i]
	}
	return flag
}

// optionKind classifies an option against FFmpeg's own option table
// (expert_options_gen.go, generated from the binary): a boolean, one that
// takes a value, or unknown. FAIL CLOSED: an option the table does not know is
// refused rather than guessed at, because a guess either way is a bypass -- a
// boolean read as value-taking swallows the next token, so a path after it
// stops reading as a second output. "-no" negations of a boolean are booleans.
func optionKind(flag string) (boolean, known bool) {
	base := optionBase(flag)
	if ffmpegBoolOptions[base] {
		return true, true
	}
	if strings.HasPrefix(base, "-no") && ffmpegBoolOptions["-"+strings.TrimPrefix(base, "-no")] {
		return true, true
	}
	if ffmpegValueOptions[base] {
		return false, true
	}
	return false, false
}

// isOption reports whether a token is an option rather than a value or a
// positional. "-" alone is FFmpeg's name for stdout, a positional output.
func isOption(a string) bool { return len(a) > 1 && a[0] == '-' }

// CheckExpertFileAccess walks one side's argv as FFmpeg would and refuses the first
// thing in it that opens a file the operator named, or that it cannot classify.
func CheckExpertFileAccess(field string, argv []string) error {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !isOption(a) {
			return fmt.Errorf("%s: %q is not the value of any option, so FFmpeg would treat it as "+
				"another output and create it as a file on the server. The destination's URL is "+
				"its only output", field, a)
		}
		base := optionBase(a)
		if why, ok := fileOptions[base]; ok {
			return fmt.Errorf("%s: %s %s. Expert mode cannot touch files on the server", field, a, why)
		}
		if base == "-i" {
			// Checked here and not only by the API: this walk also runs at
			// spawn, and a stored -i reads any file or device into the output.
			return fmt.Errorf("%s: -i adds an input, which can read a file or device on the "+
				"server. The destination's input is the relay and cannot be changed here", field)
		}
		if ffmpegNestedOptions[base] {
			return fmt.Errorf("%s: %s passes its own option list to another component, past "+
				"every check on this page. Expert mode cannot accept it", field, a)
		}
		if ffmpegFileishOptions[base] {
			return fmt.Errorf("%s: %s names a file, path, URL or log in FFmpeg's own help for it. "+
				"Expert mode cannot touch files on the server", field, a)
		}
		boolean, known := optionKind(a)
		if !known {
			return fmt.Errorf("%s: %s is not an option this server's FFmpeg table knows, so it "+
				"cannot tell whether the token after it is a value or another output. Check the "+
				"spelling; expert mode refuses what it cannot classify", field, a)
		}
		if boolean {
			continue
		}
		if i+1 >= len(argv) {
			return fmt.Errorf("%s: %s needs a value, and there is none after it", field, a)
		}
		i++
		v := argv[i]
		switch {
		case base == "-f":
			allowed, side := outputFormats, "output"
			if strings.HasPrefix(field, "input") {
				allowed, side = inputFormats, "input"
			}
			if !allowed[strings.ToLower(v)] {
				return fmt.Errorf("%s: -f %s is not one of the %s formats expert mode allows; "+
					"other formats and devices can write or read files on the server", field, v, side)
			}
		case base == "-apad":
			// -apad hands its value to the apad filter as option text, where
			// the /option= form loads a value from a file.
			if fileValuedFilterOption.MatchString(v) {
				return fmt.Errorf("%s: -apad %s loads a value from a file (the /option= form). "+
					"Expert mode cannot touch files on the server", field, v)
			}
		case filterGraphFlags[base]:
			if err := checkFilterGraph(v); err != nil {
				return fmt.Errorf("%s: the filter graph after %s %v. Expert mode cannot touch "+
					"files on the server", field, a, err)
			}
		case codecParamFlags[base]:
			if key := codecParamOpensFile(v); key != "" {
				return fmt.Errorf("%s: %s= in %s is not one of the encoder keys expert mode "+
					"allows; keys outside that list can name a file on the server", field, key, a)
			}
		}
	}
	return nil
}

// fileValuedFilterOption matches FFmpeg 7.0's "/option=path" syntax inside a
// filter's arguments, which loads that option's value from a file whatever the
// filter is: the graph-level twin of the "-/" prefix refused above.
var fileValuedFilterOption = regexp.MustCompile(`(^|[=:])\s*/`)

// checkFilterGraph refuses a graph that names any filter outside
// allowedFilters, or loads an option value from a file.
//
// It splits MORE eagerly than FFmpeg does, never less: quotes and backslashes
// are dropped first, and every ',' and ';' ends a filter. A quoted or escaped
// comma inside an argument therefore produces a spurious "filter" that is not
// on the list and is refused -- over-refusal, the safe direction. Every real
// filter boundary is a boundary here too, so a real filter cannot hide.
func checkFilterGraph(graph string) error {
	g := strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(graph)
	for _, chain := range strings.Split(g, ";") {
		for _, spec := range strings.Split(chain, ",") {
			spec = strings.TrimSpace(spec)
			// Leading input labels: [in][0:a:3]volume=...
			for strings.HasPrefix(spec, "[") {
				end := strings.IndexByte(spec, ']')
				if end < 0 {
					return fmt.Errorf("has an unterminated label in %q", spec)
				}
				spec = strings.TrimSpace(spec[end+1:])
			}
			if spec == "" {
				continue
			}
			nameEnd := strings.IndexAny(spec, "=@[ \t")
			name, args := spec, ""
			if nameEnd >= 0 {
				name, args = spec[:nameEnd], spec[nameEnd:]
			}
			if !allowedFilters[strings.ToLower(name)] {
				return fmt.Errorf("uses %q, which is not on expert mode's list of filters known "+
					"not to open files", name)
			}
			if fileValuedFilterOption.MatchString(args) {
				return fmt.Errorf("loads a value of %s from a file (the /option= form)", name)
			}
		}
	}
	return nil
}

// codecParamOpensFile returns the first key in an x264/x265-style
// "k=v:k=v" list that is not on the allowlist, or "".
//
// Keys are normalised the way the encoders read them before the lookup: the
// list is unescaped by av_dict_parse_string (so \ ' and " vanish), x264, x265
// and SVT-AV1 accept '_' for '-' and any case, and a "no-" or "no" prefix
// negates a boolean. A negation is accepted only when the key it negates is
// allowed, so no-stats and nostats are refused like stats.
func codecParamOpensFile(params string) string {
	clean := strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(params)
	for _, kv := range strings.FieldsFunc(clean, func(r rune) bool { return r == ':' || r == ',' }) {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "_", "-")
		if k == "" || codecParamKeys[k] {
			continue
		}
		if base, ok := strings.CutPrefix(k, "no-"); ok && codecParamKeys[base] {
			continue
		}
		if base, ok := strings.CutPrefix(k, "no"); ok && codecParamKeys[base] {
			continue
		}
		return k
	}
	return ""
}
