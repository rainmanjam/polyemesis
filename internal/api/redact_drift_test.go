package api

import (
	"encoding/json"
	"reflect"
	"sort"
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
	// sSecret is a credential. readSafe* must remove it outright -- by blanking
	// it, or, for a field tagged `omitempty` where a blank would delete the key
	// and change the response's shape, by putting alerts.Mask in it. See
	// redactInPlace.
	sSecret
	// sMasked carries a credential INSIDE a larger useful string -- a URL whose
	// userinfo is the password. readSafe* must mask it, leaving enough for an
	// operator to recognise the endpoint.
	sMasked
)

// leafSensitivity is the table. One entry per JSON leaf of db.Settings,
// db.Destination and db.Source, prefixed by which type it belongs to.
var leafSensitivity = map[string]sensitivity{
	"destination.accountId":                  sPublic,
	"destination.audio.codec":                sPublic,
	"destination.audio.copy":                 sPublic,
	"destination.audio.mono":                 sPublic,
	"destination.audioBitrate":               sPublic,
	"destination.backupIngestWanted":         sPublic,
	"destination.backupStreamKey":            sSecret,
	"destination.backupUrl":                  sMasked,
	"destination.compliance.facebookPrivacy": sPublic,
	"destination.compliance.labels":          sPublic,
	"destination.compliance.madeForKids":     sPublic,
	"destination.compliance.privacy":         sPublic,
	"destination.createdAt":                  sPublic,
	"destination.enabled":                    sPublic,
	"destination.expertAckReencode":          sPublic,
	// The FFmpeg arguments an operator hand-wrote for this destination. Not a
	// credential by their name and a credential by the product's own ruling:
	// GET /destinations/{id}/expert is 403 to a read token BECAUSE the argv it
	// returns carries the stream key, and these two strings are that argv as it
	// was typed. Two routes cannot give the same principal two answers about
	// the same bytes.
	"destination.extraInputArgs":                     sSecret,
	"destination.extraOutputArgs":                    sSecret,
	"destination.facebook.announcements.broadcastId": sPublic,
	"destination.facebook.announcements.occurrence":  sPublic,
	"destination.facebook.announcements.scheduleId":  sPublic,
	"destination.facebook.broadcastId":               sPublic,
	"destination.facebook.crosspost.createPost":      sPublic,
	"destination.facebook.crosspost.pageId":          sPublic,
	"destination.facebook.donateCharityId":           sPublic,
	"destination.facebook.scheduledFor":              sPublic,
	"destination.id":                                 sPublic,
	// The broadcast-lifecycle coordinator's bookkeeping. All four are public,
	// and each for its own reason rather than by association:
	//
	// broadcastId is the id in the public watch URL -- the same value
	// destination.facebook.broadcastId already carries, and the one string an
	// operator needs to find the broadcast in the platform's own console.
	// phase is the platform's own word, visible in that console to anyone who
	// can see the channel. attempts is a counter.
	//
	// fault is the only one worth arguing about, because it is free text built
	// from platform errors -- and it is public because it EXISTS to be read: it
	// is what the destination card shows when a broadcast will not start, so
	// masking it would leave an operator with a blank space instead of a
	// sentence telling them their channel is at its concurrent-broadcast limit.
	// Nothing on the path that builds it touches a key: see
	// TestALifecycleFaultCarriesNoCredential.
	"destination.lifecycle.attempts":    sPublic,
	"destination.lifecycle.broadcastId": sPublic,
	"destination.lifecycle.fault":       sPublic,
	"destination.lifecycle.phase":       sPublic,
	// The reason a stream key could not be decrypted on this machine. sPublic
	// despite sitting next to two sSecret leaves and despite the word "key"
	// being in it: it is a fixed instruction to the operator -- re-enter the
	// key -- and carries no part of the credential, not the plaintext, not the
	// ciphertext and not the failure's own text. Masking it would blank the
	// only explanation a flagged destination has, in the read-safe views that
	// are exactly where somebody debugging one would look.
	"destination.keyUnreadable":                      sPublic,
	"destination.kind":                               sPublic,
	"destination.multitrack":                         sPublic,
	"destination.name":                               sPublic,
	"destination.platform":                           sPublic,
	"destination.position":                           sPublic,
	"destination.profile.delayMs":                    sPublic,
	"destination.profile.ducking.attackMs":           sPublic,
	"destination.profile.ducking.ratio":              sPublic,
	"destination.profile.ducking.releaseMs":          sPublic,
	"destination.profile.ducking.target":             sPublic,
	"destination.profile.ducking.thresholdDb":        sPublic,
	"destination.profile.ducking.trigger":            sPublic,
	"destination.profile.excludeRoles":               sPublic,
	"destination.profile.loudness.rangeLu":           sPublic,
	"destination.profile.loudness.targetLufs":        sPublic,
	"destination.profile.loudness.truePeakDb":        sPublic,
	"destination.profile.matrix.channel":             sPublic,
	"destination.profile.matrix.gain":                sPublic,
	"destination.profile.matrix.out":                 sPublic,
	"destination.profile.matrix.track":               sPublic,
	"destination.profile.mode":                       sPublic,
	"destination.profile.normalize":                  sPublic,
	"destination.profile.sampleRate":                 sPublic,
	"destination.profile.tracks.enabled":             sPublic,
	"destination.profile.tracks.gain":                sPublic,
	"destination.profile.tracks.track":               sPublic,
	"destination.renditionId":                        sPublic,
	"destination.resilience.giveUpAfter":             sPublic,
	"destination.resilience.maxBackoffSeconds":       sPublic,
	"destination.resilience.minBackoffSeconds":       sPublic,
	"destination.sourceId":                           sPublic,
	"destination.streamKey":                          sSecret,
	"destination.transport.muxQueueBytes":            sPublic,
	"destination.transport.muxQueuePackets":          sPublic,
	"destination.transport.noDurationFilesize":       sPublic,
	"destination.transport.rwTimeoutSeconds":         sPublic,
	"destination.updatedAt":                          sPublic,
	"destination.url":                                sMasked,
	"destination.vodProfile.delayMs":                 sPublic,
	"destination.vodProfile.ducking.attackMs":        sPublic,
	"destination.vodProfile.ducking.ratio":           sPublic,
	"destination.vodProfile.ducking.releaseMs":       sPublic,
	"destination.vodProfile.ducking.target":          sPublic,
	"destination.vodProfile.ducking.thresholdDb":     sPublic,
	"destination.vodProfile.ducking.trigger":         sPublic,
	"destination.vodProfile.excludeRoles":            sPublic,
	"destination.vodProfile.loudness.rangeLu":        sPublic,
	"destination.vodProfile.loudness.targetLufs":     sPublic,
	"destination.vodProfile.loudness.truePeakDb":     sPublic,
	"destination.vodProfile.matrix.channel":          sPublic,
	"destination.vodProfile.matrix.gain":             sPublic,
	"destination.vodProfile.matrix.out":              sPublic,
	"destination.vodProfile.matrix.track":            sPublic,
	"destination.vodProfile.mode":                    sPublic,
	"destination.vodProfile.normalize":               sPublic,
	"destination.vodProfile.sampleRate":              sPublic,
	"destination.vodProfile.tracks.enabled":          sPublic,
	"destination.vodProfile.tracks.gain":             sPublic,
	"destination.vodProfile.tracks.track":            sPublic,
	"settings.alerts.retryAttempts":                  sPublic,
	"settings.automod.enabled":                       sPublic,
	"settings.automod.history.action":                sPublic,
	"settings.automod.history.idleEvictionSeconds":   sPublic,
	"settings.automod.history.maxCapsRatio":          sPublic,
	"settings.automod.history.maxLinks":              sPublic,
	"settings.automod.history.maxMentionsPerMessage": sPublic,
	"settings.automod.history.maxMessages":           sPublic,
	"settings.automod.history.maxRepeats":            sPublic,
	"settings.automod.history.minLengthForCaps":      sPublic,
	"settings.automod.history.retainPerAuthor":       sPublic,
	"settings.automod.history.timeoutSeconds":        sPublic,
	"settings.automod.history.windowSeconds":         sPublic,
	"settings.automod.model.action":                  sPublic,
	"settings.automod.model.enabled":                 sPublic,
	// Free text an operator pastes, and the shape a self-hosted or proxied
	// inference endpoint most often arrives in is
	// https://host/v1/chat/completions?api_key=sk-... The sealed key table
	// protects the key entered THERE, not one pasted into this field, and this
	// row said sPublic because the block's derived hasApiKey boolean was taken
	// as settling the whole block.
	"settings.automod.model.endpoint":        sMasked,
	"settings.automod.model.hasApiKey":       sPublic,
	"settings.automod.model.instruction":     sPublic,
	"settings.automod.model.maxCallsPerHour": sPublic,
	"settings.automod.model.minConfidence":   sPublic,
	"settings.automod.model.model":           sPublic,
	"settings.automod.model.timeoutForBan":   sPublic,
	"settings.automod.model.timeoutSeconds":  sPublic,
	"settings.automod.on":                    sPublic,
	"settings.automod.platformEnabled":       sPublic,
	"settings.automod.rules.action":          sPublic,
	"settings.automod.rules.enabled":         sPublic,
	"settings.automod.rules.id":              sPublic,
	"settings.automod.rules.name":            sPublic,
	"settings.automod.rules.pattern":         sPublic,
	"settings.automod.rules.timeoutSeconds":  sPublic,
	"settings.chat.historyMessages":          sPublic,
	"settings.chat.keepMessages":             sPublic,
	"settings.chat.purgeMinutes":             sPublic,
	"settings.chat.retentionHours":           sPublic,
	"settings.destinations.staggerMs":        sPublic,
	// The declared GPU inventory. PUBLIC, every field, and deliberately: it is
	// a description of the machine's hardware, which is the same class of fact
	// as ffmpeg.GPUInfo -- already served in full by GET
	// /api/v1/renditions/hardware. Nothing here authenticates anything. The
	// credential in this feature is the MINTED stream key, which is never
	// stored at all: it is minted per negotiation and lives only in the
	// supervisor's secret set for the life of that process.
	"settings.multitrack.gpus.dedicatedVideoMemory":          sPublic,
	"settings.multitrack.gpus.deviceId":                      sPublic,
	"settings.multitrack.gpus.driverVersion":                 sPublic,
	"settings.multitrack.gpus.model":                         sPublic,
	"settings.multitrack.gpus.sharedSystemMemory":            sPublic,
	"settings.multitrack.gpus.vendorId":                      sPublic,
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

	// One sentinel per public leaf, distinct from the credential one, so a
	// failure names which side of the rule broke. See the survival check below.
	const public = "PLANTED-PUBLIC-LEAF-4c1d"

	for _, c := range cases {
		var plantedCount int
		var publicPaths []string
		val := reflect.New(reflect.TypeOf(c.build())).Elem()
		for path, class := range leafSensitivity {
			if !strings.HasPrefix(path, c.prefix+".") {
				continue
			}
			leaf := strings.TrimPrefix(path, c.prefix+".")
			if class == sPublic {
				// Best-effort: only string leaves can carry a sentinel, and
				// only those reachable through plain struct fields -- the ones
				// behind a slice would need an element to exist first. Whatever
				// lands is enough, and the count is asserted below so "nothing
				// landed" cannot pass silently.
				if setLeafIfString(val, leaf, public) {
					publicPaths = append(publicPaths, path)
				}
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
			setLeaf(t, val, leaf, value)
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

		// AND THE SCRUB IS NOT A WHOLESALE WIPE. What stood here was
		//
		//	if reflect.DeepEqual(unscrubbed, c.scrub(unscrubbed)) { ... }
		//
		// which asks only whether the scrubber changed ANYTHING. Mutation
		// testing found what that permits: readSafeSettings and
		// readSafeDestination could be replaced by `return db.Settings{}` --
		// return the zero value, throw the whole object away -- and the entire
		// api package stayed green. Every credential is certainly gone, and so
		// is the half of the feature that says a read-only token can still
		// READ. A test whose name is "not a wholesale wipe" was satisfied by a
		// wholesale wipe.
		//
		// So the property is asserted directly: a value the table calls public
		// must SURVIVE the scrub, by name, in the marshalled body.
		if len(publicPaths) == 0 {
			t.Fatalf("%s: no public leaf could be planted, so the survival check below "+
				"proves nothing", c.prefix)
		}
		if !strings.Contains(string(raw), public) {
			t.Errorf("%s: NOT ONE of the %d public leaves survived readSafe%s "+
				"(tried %v).\nThe scrubber is wiping the object rather than removing its "+
				"credentials, and a read-only token that can read nothing is not the "+
				"feature.\nbody: %s",
				c.prefix, len(publicPaths),
				strings.ToUpper(c.prefix[:1])+c.prefix[1:], publicPaths, raw)
		}
	}
}

// setLeafIfString is setLeaf without the fatal: it reports false rather than
// failing when the path names something it cannot plant into -- a non-string
// leaf, or one behind a slice or map that would need an element created first.
//
// Separate from setLeaf rather than a flag on it, because the two callers want
// opposite things from a miss. A credential leaf that cannot be planted means
// the guard is not covering it and the test must stop; a public leaf that
// cannot be planted is simply one of many, and the caller counts what landed.
func setLeafIfString(v reflect.Value, path, value string) bool {
	name, rest, more := strings.Cut(path, ".")
	if v.Kind() != reflect.Struct {
		return false
	}
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
			return setLeafIfString(fv, rest, value)
		}
		if fv.Kind() != reflect.String || !fv.CanSet() {
			return false
		}
		fv.SetString(value)
		return true
	}
	return false
}

// TestScrubbingIsNotAWholesaleWipe pins the field set, and it is the guard this
// round had to REPAIR before it could be trusted with the answer.
//
// The claim it enforces is #150's second property: redaction blanks and masks
// VALUES, it never removes fields, so a read-scoped response has the same wire
// shape as an admin one and read-modify-write keeps working under
// DisallowUnknownFields.
//
// The version that shipped could not have caught a violation of that claim. It
// compared the TOP-LEVEL KEYS of `db.Destination{}` against the top-level keys
// of `readSafeDestination(db.Destination{})`. Both arguments are the ZERO
// VALUE, so every `omitempty` field was already absent on both sides, and
// blanking one changed nothing it looked at. destination.backupStreamKey is
// `json:"backupStreamKey,omitempty"` and readSafeDestination sets it to "": for
// a read token that field VANISHED from the response, falsifying the very claim
// this test's name asserts, while the test stayed green. It was also comparing
// only the outermost level, so nothing nested could ever have failed it either.
//
// A test asserting over a fixture that cannot exhibit the thing it checks. The
// PR had already found and fixed one instance of exactly this
// (TestReadScopedTokenCannotReadAPublishToken) and this is the second; the
// poster assertion in the playout gate matrix is the third, and G2's
// wholesale-wipe check below was the fourth. It is the failure mode of this
// codebase's tests, so it is worth naming rather than just fixing.
//
// The repair is both halves: POPULATED structs, and FULL JSON PATHS.
func TestScrubbingIsNotAWholesaleWipe(t *testing.T) {
	cases := []struct {
		name string
		// build returns a POPULATED value of the type. Every string is
		// non-empty, every number non-zero, every slice has an element, so an
		// omitempty field is actually present before the scrub and its
		// disappearance afterwards is visible.
		build func() any
		scrub func(any) any
		// exceptions are JSON paths allowed to differ, each with the reason.
		// Deliberately a map with prose in it rather than a silent skip: a
		// structural difference that is intended has to be argued for once,
		// where the next reader will find it.
		exceptions map[string]string
	}{
		{
			name:  "settings",
			build: func() any { return populated[db.Settings](t) },
			scrub: func(v any) any { return readSafeSettings(v.(db.Settings)) },
		},
		{
			name:  "destination",
			build: func() any { return populated[db.Destination](t) },
			scrub: func(v any) any { return readSafeDestination(v.(db.Destination)) },
		},
		{
			name:  "source",
			build: func() any { return populated[db.Source](t) },
			scrub: func(v any) any { return readSafeSource(v.(db.Source)) },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := c.build()
			before := jsonPaths(t, v)
			after := jsonPaths(t, c.scrub(v))

			for _, p := range before {
				if contains(after, p) || excepted(c.exceptions, p) {
					continue
				}
				t.Errorf("%s: the JSON path %q is present for an admin and ABSENT for a "+
					"read token.\nThat is a field REMOVED, not a value blanked. Almost "+
					"always the field carries `omitempty` and the scrubber set it to the "+
					"zero value; redact it IN PLACE instead -- put alerts.Mask in it, or "+
					"mask the URL -- so the key survives with its content gone. "+
					"If the removal is genuinely intended, add it to this case's "+
					"exceptions with the argument.", c.name, p)
			}
			for _, p := range after {
				if !contains(before, p) && !excepted(c.exceptions, p) {
					t.Errorf("%s: the JSON path %q appears only for a read token. "+
						"The scrubber is adding a field.", c.name, p)
				}
			}
		})
	}
}

// TestViewShapesAreIdenticalByPrincipal is the same claim for the two VIEW
// types, which the leaf-classification guard never covered and which is where
// the second vanishing field was.
//
// sourceView and playoutAdminView are not stored types, so they are not in the
// leaf table, and their redaction used to be written inline in viewSource and
// handleGetPlayout. That is exactly why they needed asserting:
// sourceView.LegacyRTMPKey is `json:"legacyRtmpKey,omitempty"` and viewSource
// set it to "" for a read token, so the field DISAPPEARED -- the same species as
// backupStreamKey, in a place no guard was looking.
//
// IT DRIVES THE PRODUCTION PROJECTION. The previous version of this test did
// not: it asserted against redactSourceViewLikeViewSource, a hand copy of the
// handler's lines living in this file. Reverting internal/api/sources.go from
// `legacyKey = redactInPlace(legacyKey)` to the pre-fix `legacyKey = ""` left
// `go test ./...` green across the whole repository -- measured. A guard that
// watches a copy is decorative, and the copy IS the bug. Both restatements are
// deleted; readSafeSourceView and readSafePlayoutView are pure functions of the
// view for the sole purpose of being callable from here.
//
// THE SHAPE CHECK IS NOT ENOUGH ON ITS OWN and does not pretend to be. It
// compares JSON path SETS, not values, so `legacyKey = legacyKey` would pass it.
// The value half is the route sweep, which scans real read-bearer bytes for
// planted sentinels. /api/v1/sources therefore carries BOTH counterparts in the
// coverage ledger, and neither is sufficient alone.
func TestViewShapesAreIdenticalByPrincipal(t *testing.T) {
	cases := []struct {
		name       string
		full       any
		redacted   any
		exceptions map[string]string
		// mustPopulate are JSON paths the fixture is REQUIRED to produce on the
		// admin side. This is the anti-vacuity check: every assertion in this
		// test is of the form "a path present before is present after", and a
		// path that was never present before is silently vacuous. The field this
		// test exists for -- legacyRtmpKey -- is precisely one that vanishes
		// when unpopulated, so a fill() that stopped reaching it would turn the
		// whole case into a tautology.
		mustPopulate []string
	}{
		{
			name:     "sourceView",
			full:     populated[sourceView](t),
			redacted: readSafeSourceView(populated[sourceView](t)),
			exceptions: map[string]string{
				// publishUrls is a MAP, and the projection nils it wholesale
				// rather than masking its values. That is deliberate and cannot
				// be otherwise: each entry is a publish URL in which the token
				// IS the address -- srt://host?streamid=TOKEN -- so there is no
				// masked form of it that remains a URL. The key survives as
				// null; what disappears is the per-protocol keys UNDER it, and
				// those are derived names rather than stored fields, so no
				// client reads them back.
				"publishUrls": "a derived map whose every value embeds the token as its address",
			},
			mustPopulate: []string{"legacyRtmpKey", "token", "publishUrls"},
		},
		{
			name:         "playoutAdminView",
			full:         populated[playoutAdminView](t),
			redacted:     readSafePlayoutView(populated[playoutAdminView](t)),
			mustPopulate: []string{"token", "urls.master", "urls.watch", "urls.embed"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := jsonPaths(t, c.full)
			after := jsonPaths(t, c.redacted)

			// ANTI-VACUITY FIRST. If the fixture does not populate the field,
			// nothing below is asserting anything about it.
			for _, p := range c.mustPopulate {
				if !contains(before, p) {
					t.Errorf("%s: the fixture does not populate %q, so every assertion "+
						"below about that path is vacuous. This test exists BECAUSE an "+
						"omitempty field disappeared unnoticed; a fixture that leaves it "+
						"unset reproduces the blindness it was written to end.", c.name, p)
				}
			}

			for _, p := range before {
				if !contains(after, p) && !excepted(c.exceptions, p) {
					t.Errorf("%s: the JSON path %q is present for an admin and ABSENT for "+
						"a read token. The field is being removed rather than blanked; "+
						"redact it in place so the key survives.", c.name, p)
				}
			}
			for _, p := range after {
				if !contains(before, p) && !excepted(c.exceptions, p) {
					t.Errorf("%s: the JSON path %q appears only for a read token.", c.name, p)
				}
			}
		})
	}
}

// populated returns a T with every leaf set to a non-zero value, so that a
// field carrying `omitempty` is actually PRESENT in the marshalled form and its
// removal by a scrubber is observable.
func populated[T any](t *testing.T) T {
	t.Helper()
	var v T
	fill(t, reflect.ValueOf(&v).Elem(), 0)
	return v
}

// fill sets every settable leaf under v to something non-zero.
//
// depth bounds the recursion rather than tracking visited types: the stored
// types are trees, but a self-referential one would otherwise hang the test
// rather than fail it, and a hang in CI is much harder to read than a failure.
func fill(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	if depth > 12 || !v.CanSet() {
		return
	}
	// A type with its own MarshalJSON decides its own wire form, and its
	// unexported fields are not ours to set. time.Time is the one that reaches
	// here; its zero value still marshals to a non-empty string, so leaving it
	// alone does not reintroduce the zero-value blindness.
	if v.Type().Implements(reflect.TypeFor[json.Marshaler]()) ||
		reflect.PointerTo(v.Type()).Implements(reflect.TypeFor[json.Marshaler]()) {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(t, v.Elem(), depth+1)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(t, v.Index(0), depth+1)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		fill(t, key, depth+1)
		elem := reflect.New(v.Type().Elem()).Elem()
		fill(t, elem, depth+1)
		v.SetMapIndex(key, elem)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(t, v.Field(i), depth+1)
			}
		}
	}
}

// jsonPaths marshals v and returns every JSON path in it, sorted.
//
// Paths and not top-level keys, because a credential nested three levels down
// is exactly as capable of vanishing as one at the root and the shipped guard
// could not see either. Array elements collapse to a single "[]" segment: the
// question is which FIELDS exist, and one element answers it.
func jsonPaths(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var out []string
	var walk func(prefix string, node any)
	walk = func(prefix string, node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, child := range n {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				out = append(out, p)
				walk(p, child)
			}
		case []any:
			if len(n) > 0 {
				walk(prefix+"[]", n[0])
			}
		}
	}
	walk("", doc)
	sort.Strings(out)
	return out
}

// excepted reports whether a path is covered by an exception, either exactly or
// as something nested under one. Prefix-aware because an exception about a
// container has to cover the container's contents: naming every key inside a
// MAP would mean writing data down as though it were schema, and the next
// fixture change would silently invalidate it.
func excepted(exceptions map[string]string, path string) bool {
	for k := range exceptions {
		if path == k || strings.HasPrefix(path, k+".") || strings.HasPrefix(path, k+"[]") {
			return true
		}
	}
	return false
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
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
		// UNEXPORTED USUALLY MEANS "NEVER ON THE WIRE", AND EMBEDDING IS THE
		// EXCEPTION. encoding/json promotes the EXPORTED fields of an anonymous
		// embedded struct even when the embedded type itself is unexported, so
		// `struct{ announcementSet }` puts announcementSet's exported leaves in
		// the enclosing object under their own names. Skipping the field would
		// have hidden every one of them from this guard -- leaves that are
		// stored, readable, and unchecked, which is the precise blind spot this
		// walk exists to close.
		//
		// Only structs. An unexported embedded field of any other type really
		// is dropped by encoding/json, and so is any ordinary unexported field.
		if !f.IsExported() {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if !f.Anonymous || et.Kind() != reflect.Struct {
				continue
			}
		}
		tag := f.Tag.Get("json")
		// json:"-" never reaches the wire, which is the pattern platform client
		// secrets and OAuth account tokens already use correctly.
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		// An ANONYMOUS embedded struct whose json tag names nothing is INLINED
		// by encoding/json, so its leaves are keys of the ENCLOSING object and
		// the type's name is on no wire. The classification table is an
		// inventory of stored leaf PATHS, so a walk that invented a segment here
		// would retire every real entry as dead and demand the same leaves be
		// classified again under a name that cannot be read back --
		// destination.facebook embeds db.AnnouncementSet.
		//
		// Only when the tag names nothing: an embedded field tagged `json:"x"`
		// really is nested one level down.
		// INLINED MEANS EMBEDDED STRUCT, AND encoding/json IS STRICTER THAN
		// "anonymous with no tag". An embedded *struct is inlined too, but an
		// embedded NAMED SLICE or MAP is not -- `type Tags []string` embedded
		// anonymously nests under "Tags" and its elements are not keys of the
		// enclosing object. The test below was computed BEFORE the deref loop,
		// which strips slices and maps as well as pointers, so such a field
		// would have been walked as inlined and every path under it would have
		// been off by one segment.
		//
		// Nothing in the tree embeds a named slice today. This matters because
		// THIS diff is what makes anonymous embedding the sharing pattern here:
		// the next person to reach for it gets a walker that agrees with the
		// bytes, or a guard that quietly checks the wrong paths.
		//
		// So the inline test sees through POINTERS ONLY and stops there.
		embedded := f.Type
		for embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		inlined := f.Anonymous && name == "" && embedded.Kind() == reflect.Struct
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
			if inlined {
				leafWalk(t, ft, prefix, visit)
			} else {
				leafWalk(t, ft, path, visit)
			}
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
