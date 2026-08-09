package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The drift guard: a NEW credential field must not be able to become
// token-reachable in silence.
//
// The two tests above this one prove the credentials that exist today do not
// reach a read token. Neither of them can say anything about a field added next
// month, and that is precisely how #150 happened -- ingest.rtmp.streamKey and
// ingest.srt.passphrase were already there when viewSource was written, and the
// author redacted the three fields they were thinking about.
//
// So every JSON leaf of the three credential-bearing stored types is
// CLASSIFIED HERE, BY HAND. Adding a field to db.Settings, db.Destination or
// db.Source fails this test until somebody writes down what it is. That is the
// whole mechanism: the classification is the work, and the test only enforces
// that it was done.
//
// NOT A NAME HEURISTIC, deliberately and by measurement. alerts.SecretName --
// the obvious candidate, and one this repository already ships -- returns FALSE
// for "backupStreamKey", so a guard built on it would have shipped two of the
// three destination leaks while reporting success. A table is more typing and
// it cannot be wrong about a name it has never seen, because it has to have
// seen every name.

type sensitivity int

const (
	// sPublic is safe for any authenticated principal, including a read token.
	sPublic sensitivity = iota
	// sSecret is a credential. readSafe* must blank it outright.
	sSecret
	// sMasked carries a credential INSIDE a larger useful string -- a URL whose
	// userinfo is the password. readSafe* must mask it, leaving enough for an
	// operator to recognise the endpoint.
	sMasked
)

// leafSensitivity is the table. One entry per JSON leaf of db.Settings,
// db.Destination and db.Source, prefixed by which type it belongs to.
var leafSensitivity = map[string]sensitivity{
	"destination.accountId":                                  sPublic,
	"destination.audio.codec":                                sPublic,
	"destination.audio.mono":                                 sPublic,
	"destination.audioBitrate":                               sPublic,
	"destination.backupIngestWanted":                         sPublic,
	"destination.backupStreamKey":                            sSecret,
	"destination.backupUrl":                                  sMasked,
	"destination.compliance.facebookPrivacy":                 sPublic,
	"destination.compliance.labels":                          sPublic,
	"destination.compliance.madeForKids":                     sPublic,
	"destination.compliance.privacy":                         sPublic,
	"destination.createdAt":                                  sPublic,
	"destination.enabled":                                    sPublic,
	"destination.expertAckReencode":                          sPublic,
	"destination.extraInputArgs":                             sPublic,
	"destination.extraOutputArgs":                            sPublic,
	"destination.facebook.announcements.broadcastId":         sPublic,
	"destination.facebook.announcements.occurrence":          sPublic,
	"destination.facebook.announcements.scheduleId":          sPublic,
	"destination.facebook.broadcastId":                       sPublic,
	"destination.facebook.crosspost.createPost":              sPublic,
	"destination.facebook.crosspost.pageId":                  sPublic,
	"destination.facebook.donateCharityId":                   sPublic,
	"destination.facebook.scheduledFor":                      sPublic,
	"destination.id":                                         sPublic,
	"destination.kind":                                       sPublic,
	"destination.name":                                       sPublic,
	"destination.platform":                                   sPublic,
	"destination.position":                                   sPublic,
	"destination.profile.delayMs":                            sPublic,
	"destination.profile.ducking.attackMs":                   sPublic,
	"destination.profile.ducking.ratio":                      sPublic,
	"destination.profile.ducking.releaseMs":                  sPublic,
	"destination.profile.ducking.target":                     sPublic,
	"destination.profile.ducking.thresholdDb":                sPublic,
	"destination.profile.ducking.trigger":                    sPublic,
	"destination.profile.excludeRoles":                       sPublic,
	"destination.profile.loudness.rangeLu":                   sPublic,
	"destination.profile.loudness.targetLufs":                sPublic,
	"destination.profile.loudness.truePeakDb":                sPublic,
	"destination.profile.matrix.channel":                     sPublic,
	"destination.profile.matrix.gain":                        sPublic,
	"destination.profile.matrix.out":                         sPublic,
	"destination.profile.matrix.track":                       sPublic,
	"destination.profile.mode":                               sPublic,
	"destination.profile.normalize":                          sPublic,
	"destination.profile.sampleRate":                         sPublic,
	"destination.profile.tracks.enabled":                     sPublic,
	"destination.profile.tracks.gain":                        sPublic,
	"destination.profile.tracks.track":                       sPublic,
	"destination.renditionId":                                sPublic,
	"destination.resilience.giveUpAfter":                     sPublic,
	"destination.resilience.maxBackoffSeconds":               sPublic,
	"destination.resilience.minBackoffSeconds":               sPublic,
	"destination.sourceId":                                   sPublic,
	"destination.streamKey":                                  sSecret,
	"destination.transport.muxQueueBytes":                    sPublic,
	"destination.transport.muxQueuePackets":                  sPublic,
	"destination.transport.noDurationFilesize":               sPublic,
	"destination.transport.rwTimeoutSeconds":                 sPublic,
	"destination.updatedAt":                                  sPublic,
	"destination.url":                                        sMasked,
	"settings.alerts.retryAttempts":                          sPublic,
	"settings.automod.enabled":                               sPublic,
	"settings.automod.history.action":                        sPublic,
	"settings.automod.history.idleEvictionSeconds":           sPublic,
	"settings.automod.history.maxCapsRatio":                  sPublic,
	"settings.automod.history.maxLinks":                      sPublic,
	"settings.automod.history.maxMentionsPerMessage":         sPublic,
	"settings.automod.history.maxMessages":                   sPublic,
	"settings.automod.history.maxRepeats":                    sPublic,
	"settings.automod.history.minLengthForCaps":              sPublic,
	"settings.automod.history.retainPerAuthor":               sPublic,
	"settings.automod.history.timeoutSeconds":                sPublic,
	"settings.automod.history.windowSeconds":                 sPublic,
	"settings.automod.model.action":                          sPublic,
	"settings.automod.model.enabled":                         sPublic,
	"settings.automod.model.endpoint":                        sPublic,
	"settings.automod.model.hasApiKey":                       sPublic,
	"settings.automod.model.instruction":                     sPublic,
	"settings.automod.model.maxCallsPerHour":                 sPublic,
	"settings.automod.model.minConfidence":                   sPublic,
	"settings.automod.model.model":                           sPublic,
	"settings.automod.model.timeoutForBan":                   sPublic,
	"settings.automod.model.timeoutSeconds":                  sPublic,
	"settings.automod.on":                                    sPublic,
	"settings.automod.platformEnabled":                       sPublic,
	"settings.automod.rules.action":                          sPublic,
	"settings.automod.rules.enabled":                         sPublic,
	"settings.automod.rules.id":                              sPublic,
	"settings.automod.rules.name":                            sPublic,
	"settings.automod.rules.pattern":                         sPublic,
	"settings.automod.rules.timeoutSeconds":                  sPublic,
	"settings.chat.historyMessages":                          sPublic,
	"settings.chat.keepMessages":                             sPublic,
	"settings.chat.purgeMinutes":                             sPublic,
	"settings.chat.retentionHours":                           sPublic,
	"settings.destinations.staggerMs":                        sPublic,
	"settings.failover.backup.enabled":                       sPublic,
	"settings.failover.backup.mode":                          sPublic,
	"settings.failover.backup.pull.reconnectDelayMaxSeconds": sPublic,
	"settings.failover.backup.pull.rtspTransport":            sPublic,
	"settings.failover.backup.pull.url":                      sMasked,
	"settings.failover.backup.rtmp.app":                      sPublic,
	"settings.failover.backup.rtmp.streamKey":                sSecret,
	"settings.failover.backup.srt.latencyMs":                 sPublic,
	"settings.failover.backup.srt.passphrase":                sSecret,
	"settings.failover.enabled":                              sPublic,
	"settings.failover.graceSeconds":                         sPublic,
	"settings.failover.playlist.enabled":                     sPublic,
	"settings.failover.playlist.items.upload":                sPublic,
	"settings.failover.return":                               sPublic,
	"settings.failover.returnStableSeconds":                  sPublic,
	"settings.failover.slate.color":                          sPublic,
	"settings.failover.slate.enabled":                        sPublic,
	"settings.failover.slate.encoder":                        sPublic,
	"settings.failover.slate.imagePath":                      sPublic,
	"settings.failover.slate.preset":                         sPublic,
	"settings.failover.slate.videoKbps":                      sPublic,
	"settings.ingest.annotations.denoise":                    sPublic,
	"settings.ingest.annotations.label":                      sPublic,
	"settings.ingest.annotations.language":                   sPublic,
	"settings.ingest.annotations.role":                       sPublic,
	"settings.ingest.annotations.track":                      sPublic,
	"settings.ingest.mode":                                   sPublic,
	"settings.ingest.pull.reconnectDelayMaxSeconds":          sPublic,
	"settings.ingest.pull.rtspTransport":                     sPublic,
	"settings.ingest.pull.url":                               sMasked,
	"settings.ingest.rtmp.app":                               sPublic,
	"settings.ingest.rtmp.streamKey":                         sSecret,
	"settings.ingest.srt.latencyMs":                          sPublic,
	"settings.ingest.srt.passphrase":                         sSecret,
	"settings.listeners.rtmpPort":                            sPublic,
	"settings.listeners.srtPort":                             sPublic,
	"settings.logging.maxFileMb":                             sPublic,
	"settings.logging.maxFiles":                              sPublic,
	"settings.logging.persistProcessLogs":                    sPublic,
	"settings.meters.enabled":                                sPublic,
	"settings.meters.intervalMs":                             sPublic,
	"settings.mqtt.brokerUrl":                                sMasked,
	"settings.mqtt.clientId":                                 sPublic,
	"settings.mqtt.discovery":                                sPublic,
	"settings.mqtt.enabled":                                  sPublic,
	"settings.mqtt.hasPassword":                              sPublic,
	"settings.mqtt.instance":                                 sPublic,
	"settings.mqtt.intervalSeconds":                          sPublic,
	"settings.mqtt.keepAliveSeconds":                         sPublic,
	"settings.mqtt.prefix":                                   sPublic,
	"settings.mqtt.tlsSkipVerify":                            sPublic,
	"settings.mqtt.username":                                 sPublic,
	"settings.playout.allowCrossOrigin":                      sPublic,
	"settings.playout.audioKbps":                             sPublic,
	"settings.playout.dvrWindowSeconds":                      sPublic,
	"settings.playout.enabled":                               sPublic,
	"settings.playout.format":                                sPublic,
	"settings.playout.maxDiskMb":                             sPublic,
	"settings.playout.maxSessions":                           sPublic,
	"settings.playout.playlistSegments":                      sPublic,
	"settings.playout.public":                                sPublic,
	"settings.playout.segmentSeconds":                        sPublic,
	"settings.playout.sessionIdleSeconds":                    sPublic,
	"settings.playout.variants.audioTrack":                   sPublic,
	"settings.playout.variants.enabled":                      sPublic,
	"settings.playout.variants.name":                         sPublic,
	"settings.playout.variants.renditionId":                  sPublic,
	"settings.postProd.avoidGpuWhenStreaming":                sPublic,
	"settings.postProd.batteryFloorPercent":                  sPublic,
	"settings.postProd.concurrency":                          sPublic,
	"settings.postProd.cpuCeilingPercent":                    sPublic,
	"settings.postProd.cpuResumePercent":                     sPublic,
	"settings.postProd.cpuSettleSeconds":                     sPublic,
	"settings.postProd.cpuSustainedSeconds":                  sPublic,
	"settings.postProd.defaultMode":                          sPublic,
	"settings.postProd.deferSeconds":                         sPublic,
	"settings.postProd.enabled":                              sPublic,
	"settings.postProd.gpuBusy":                              sPublic,
	"settings.postProd.idleIo":                               sPublic,
	"settings.postProd.ingestLingerSeconds":                  sPublic,
	"settings.postProd.kinds.ignoreIngest":                   sPublic,
	"settings.postProd.kinds.kind":                           sPublic,
	"settings.postProd.kinds.mode":                           sPublic,
	"settings.postProd.kinds.usesGpu":                        sPublic,
	"settings.postProd.kinds.windows.days":                   sPublic,
	"settings.postProd.kinds.windows.endMinutes":             sPublic,
	"settings.postProd.kinds.windows.startMinutes":           sPublic,
	"settings.postProd.kinds.windows.tz":                     sPublic,
	"settings.postProd.niceLevel":                            sPublic,
	"settings.postProd.retainDays":                           sPublic,
	"settings.postProd.retainJobs":                           sPublic,
	"settings.postProd.thermalCeilingC":                      sPublic,
	"settings.postProd.whisperModel":                         sPublic,
	"settings.postProd.yieldToStream":                        sPublic,
	"settings.preview.enabled":                               sPublic,
	"settings.preview.idleTimeoutSeconds":                    sPublic,
	"settings.preview.segmentSeconds":                        sPublic,
	"settings.preview.videoHeight":                           sPublic,
	"settings.preview.videoKbps":                             sPublic,
	"settings.recording.enabled":                             sPublic,
	"settings.recording.maxAgeHours":                         sPublic,
	"settings.recording.maxGb":                               sPublic,
	"settings.recording.minFreeGb":                           sPublic,
	"settings.recording.segmentSeconds":                      sPublic,
	"settings.recording.stemCodec":                           sPublic,
	"settings.recording.stems":                               sPublic,
	"settings.synth.silenceOnVideoOnly":                      sPublic,
	"source.createdAt":                                       sPublic,
	"source.enabled":                                         sPublic,
	"source.id":                                              sPublic,
	"source.ingest.annotations.denoise":                      sPublic,
	"source.ingest.annotations.label":                        sPublic,
	"source.ingest.annotations.language":                     sPublic,
	"source.ingest.annotations.role":                         sPublic,
	"source.ingest.annotations.track":                        sPublic,
	"source.ingest.mode":                                     sPublic,
	"source.ingest.pull.reconnectDelayMaxSeconds":            sPublic,
	"source.ingest.pull.rtspTransport":                       sPublic,
	"source.ingest.pull.url":                                 sMasked,
	"source.ingest.rtmp.app":                                 sPublic,
	"source.ingest.rtmp.streamKey":                           sSecret,
	"source.ingest.srt.latencyMs":                            sPublic,
	"source.ingest.srt.passphrase":                           sSecret,
	"source.name":                                            sPublic,
	"source.position":                                        sPublic,
	"source.prevTokenUntil":                                  sPublic,
	"source.token":                                           sSecret,
	"source.updatedAt":                                       sPublic,
}

// TestEveryStoredLeafIsClassified is the ratchet.
//
// It walks the real reflect types rather than a copy of their field lists, so
// the table cannot drift from the structs it describes in either direction: an
// unclassified leaf fails, and an entry naming a leaf that no longer exists
// fails too.
func TestEveryStoredLeafIsClassified(t *testing.T) {
	found := map[string]bool{}
	for prefix, rt := range map[string]reflect.Type{
		"settings":    reflect.TypeOf(db.Settings{}),
		"destination": reflect.TypeOf(db.Destination{}),
		"source":      reflect.TypeOf(db.Source{}),
	} {
		leafWalk(t, rt, prefix, func(path string) {
			found[path] = true
			if _, ok := leafSensitivity[path]; !ok {
				t.Errorf("%s is a new JSON leaf on a stored type and nothing says whether "+
					"it is a credential. Classify it in leafSensitivity as sPublic, sSecret "+
					"or sMasked -- and if it is either of the latter two, teach the matching "+
					"readSafe* function in redact.go to blank or mask it. "+
					"TestReadSafeViewsScrubEverySecretLeaf will fail until you do.", path)
			}
		})
	}
	for path := range leafSensitivity {
		if !found[path] {
			t.Errorf("leafSensitivity classifies %s, which no longer exists on any stored "+
				"type. Delete the entry; a table with dead rows in it stops being readable "+
				"as an inventory.", path)
		}
	}
}

// TestReadSafeViewsScrubEverySecretLeaf is the other half, and it is the one
// that actually protects anything.
//
// The classification above is a promise. This plants a unique string into every
// leaf the table calls a credential, runs the real readSafe* function over the
// value, marshals what comes back, and fails if the string survives. A field
// classified sSecret that redact.go forgot about is caught here rather than by
// somebody reading the diff.
func TestReadSafeViewsScrubEverySecretLeaf(t *testing.T) {
	const planted = "PLANTED-SECRET-LEAF-4c1d"

	cases := []struct {
		prefix string
		build  func() any
		scrub  func(any) any
	}{
		{"settings", func() any { return db.Settings{} },
			func(v any) any { return readSafeSettings(v.(db.Settings)) }},
		{"destination", func() any { return db.Destination{} },
			func(v any) any { return readSafeDestination(v.(db.Destination)) }},
		{"source", func() any { return db.Source{} },
			func(v any) any { return readSafeSource(v.(db.Source)) }},
	}

	for _, c := range cases {
		var plantedCount int
		val := reflect.New(reflect.TypeOf(c.build())).Elem()
		for path, class := range leafSensitivity {
			if class == sPublic || !strings.HasPrefix(path, c.prefix+".") {
				continue
			}
			// A masked leaf is a URL, so the sentinel goes where a credential
			// actually lives in one: the userinfo. Planting a bare word would
			// exercise alerts.RedactURL's unparseable path instead of the
			// userinfo path, and prove the wrong thing.
			value := planted
			if class == sMasked {
				value = "rtsp://operator:" + planted + "@host.example/live"
			}
			setLeaf(t, val, strings.TrimPrefix(path, c.prefix+"."), value)
			plantedCount++
		}
		if plantedCount == 0 {
			t.Fatalf("%s: nothing was planted, so this proves nothing", c.prefix)
		}

		raw, err := json.Marshal(c.scrub(val.Interface()))
		if err != nil {
			t.Fatalf("%s: marshal the scrubbed value: %v", c.prefix, err)
		}
		if strings.Contains(string(raw), planted) {
			t.Errorf("readSafe%s left a planted credential in the marshalled body. "+
				"Some leaf classified sSecret or sMasked is not being blanked or masked "+
				"in redact.go.\nbody: %s",
				strings.ToUpper(c.prefix[:1])+c.prefix[1:], raw)
		}

		// And the scrub is not a wholesale wipe: a value the table calls public
		// has to survive, or the "read-only still means readable" half of the
		// feature is gone.
		unscrubbed := val.Interface()
		if reflect.DeepEqual(unscrubbed, c.scrub(unscrubbed)) {
			t.Errorf("%s: readSafe returned its input unchanged even though secrets "+
				"were planted in it", c.prefix)
		}
	}
}

// TestScrubbingIsNotAWholesaleWipe pins the field set.
//
// Blanking VALUES rather than reshaping the body is what lets the settings and
// destination editors keep doing read-modify-write, so it is worth asserting
// directly that the scrubbers do not add, drop or rename a single key.
func TestScrubbingIsNotAWholesaleWipe(t *testing.T) {
	for name, pair := range map[string][2]any{
		"settings":    {db.Settings{}, readSafeSettings(db.Settings{})},
		"destination": {db.Destination{}, readSafeDestination(db.Destination{})},
		"source":      {db.Source{}, readSafeSource(db.Source{})},
	} {
		before := jsonKeys(t, pair[0])
		after := jsonKeys(t, pair[1])
		if before != after {
			t.Errorf("%s: the scrubbed value has a different SHAPE.\nbefore: %s\nafter:  %s\n"+
				"A response whose field set changes by principal breaks read-modify-write "+
				"clients under DisallowUnknownFields.", name, before, after)
		}
	}
}

func jsonKeys(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	// Sorted so the comparison is about membership, not map order.
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return strings.Join(keys, ",")
}

// leafWalk visits every JSON leaf path of a struct tree.
//
// Slices and maps are followed into their ELEMENT type rather than skipped:
// settings.automod.rules and settings.playout.variants are whole blocks of
// fields that live behind a slice, and a walk that stopped at the container
// would leave them unclassified while reporting full coverage.
func leafWalk(t *testing.T, rt reflect.Type, prefix string, visit func(path string)) {
	t.Helper()
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		// json:"-" never reaches the wire, which is the pattern platform client
		// secrets and OAuth account tokens already use correctly.
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice || ft.Kind() == reflect.Map {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Time" {
			leafWalk(t, ft, path, visit)
			continue
		}
		visit(path)
	}
}

// setLeaf writes a string into a nested field addressed by its JSON path.
//
// No path in the secret or masked classes crosses a slice or a map, so this
// deliberately refuses to walk one rather than inventing a rule for which
// element to write. If a credential ever does land inside a slice, this fails
// loudly instead of quietly planting nothing and reporting a clean scrub.
func setLeaf(t *testing.T, v reflect.Value, path, value string) {
	t.Helper()
	name, rest, more := strings.Cut(path, ".")
	rt := v.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "" {
			tag = f.Name
		}
		if tag != name {
			continue
		}
		fv := v.Field(i)
		if more {
			if fv.Kind() != reflect.Struct {
				t.Fatalf("cannot walk into %s: it is a %s, not a struct", name, fv.Kind())
			}
			setLeaf(t, fv, rest, value)
			return
		}
		if fv.Kind() != reflect.String {
			t.Fatalf("%s is a %s; a credential leaf is expected to be a string", name, fv.Kind())
		}
		fv.SetString(value)
		return
	}
	t.Fatalf("no field named %q", name)
}
