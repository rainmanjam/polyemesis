package engine

import (
	"sync"

	"github.com/rainmanjam/polyemesis/internal/events"
)

// What happens when an operator changes a setting while the stream is up.
//
// This file is the answer, written down as data rather than as prose, because
// prose does not fail a build. Every silent no-op this repo has shipped had the
// same shape: a field added to db.Settings, validated, stored, returned by the
// API, and never wired to anything already running. The settings page said
// saved; the process went on doing what it was doing.
//
// The dividing line is mechanical, and it is the only reason any of this is
// safe: DOES THE VALUE REACH AN FFMPEG ARGV? If it does, the child is replaced,
// because FFmpeg 8.1.2 has no way to change a muxer, an encoder, an output URL
// or a stream mapping in flight, and the two channels it does offer -- zmq and
// sendcmd -- address only filters that were instantiated with a command
// interface and give no readable confirmation that a command was accepted. A
// half-applied graph nobody can read back is worse than a restart.
//
// If it does not reach an argv, it must be delivered to the running process,
// and the rule names the function that delivers it.

// ReloadClass is what a change to one settings field costs.
type ReloadClass string

const (
	// ClassLive is applied to whatever is already running. No child process is
	// replaced and no viewer or platform connection is dropped.
	ClassLive ReloadClass = "live"
	// ClassRespawn is baked into a child's argv. The signature named in the
	// rule notices, and the child is replaced.
	ClassRespawn ReloadClass = "respawn"
	// ClassRebind is a bound socket. The listener is stopped and rebound; every
	// publisher on it reconnects.
	ClassRebind ReloadClass = "rebind"
	// ClassOnDemand is read at the moment it is needed and never held by
	// anything. Nothing has to be applied because nothing has a copy.
	//
	// This is the class most likely to be claimed falsely, so a rule using it
	// still has to name the reader.
	ClassOnDemand ReloadClass = "on_demand"
	// ClassNextStart is stored now and acted on at the next process start.
	//
	// NOTHING USES IT, and that is the intended steady state. It was added
	// because writing this table found one -- job-history retention, read once
	// at boot, so lowering it did nothing an operator could observe until they
	// restarted -- and the choice was between recording that honestly and
	// mislabelling it as live. Recording it is what made it obvious enough to
	// fix, which took an hour once it had a name.
	//
	// Kept rather than deleted because the next one will be found the same way,
	// and a reviewer needs somewhere to put it that is not a lie. A rule in this
	// class is an admission, not a design: anything landing here should be read
	// as a candidate for ClassLive that nobody has wired yet.
	ClassNextStart ReloadClass = "next_start"
)

// ReloadRule records the decision for one settings field.
type ReloadRule struct {
	Class ReloadClass
	// Applies names the function that carries the change: the one that pushes
	// the value into a running process for ClassLive, the signature that
	// notices for ClassRespawn, the reader for ClassOnDemand. Checked against
	// the source by TestEveryReloadRuleNamesAFunctionThatExists, so it is a
	// claim rather than a comment.
	Applies string
	Why     string
}

// ClassOf reports the rule for a dotted json path, e.g. "meters.intervalMs".
func ClassOf(path string) (ReloadRule, bool) {
	r, ok := settingsReload[path]
	return r, ok
}

// settingsReload is keyed by the dotted json path of every leaf in db.Settings.
// TestEverySettingsFieldHasAReloadRule fails when a field is added without one.
var settingsReload = map[string]ReloadRule{
	// ---------------------------------------------------------------- ingest
	"ingest.mode":                          {ClassRespawn, "reconcileIngest", "chooses the listener; SRT has no child at all, so the mode change is a spawn or a kill"},
	"ingest.srt.passphrase":                {ClassRespawn, "reconcileIngest", "an SRT socket option, fixed at bind"},
	"ingest.srt.latencyMs":                 {ClassRespawn, "reconcileIngest", "an SRT socket option, fixed at bind"},
	"ingest.rtmp.app":                      {ClassRespawn, "reconcileIngest", "part of the listener's URL"},
	"ingest.rtmp.streamKey":                {ClassRespawn, "reconcileIngest", "matched against the publisher's playpath at connect"},
	"ingest.pull.url":                      {ClassRespawn, "reconcileIngest", "the input FFmpeg dials"},
	"ingest.pull.reconnectDelayMaxSeconds": {ClassRespawn, "reconcileIngest", "an FFmpeg input option, not a supervisor one"},
	"ingest.pull.rtspTransport":            {ClassRespawn, "reconcileIngest", "an FFmpeg input option"},

	// ------------------------------------------------------------- listeners
	"listeners.srtPort":  {ClassRebind, "reconcileSharedIngest", "one bound socket for every source; the Manager stops it and binds the new number"},
	"listeners.rtmpPort": {ClassRespawn, "reconcileIngest", "part of the ingest child's listen URL"},

	// ------------------------------------------------------------- recording
	"recording.enabled":        {ClassRespawn, "reconcileRecorder", "starts or stops the child"},
	"recording.segmentSeconds": {ClassRespawn, "reconcileRecorder", "the segment muxer's argv"},
	"recording.maxAgeHours":    {ClassLive, "ScanAndSweep", "a retention rule the sweeper re-reads through the settings func it was handed"},
	"recording.maxGb":          {ClassLive, "ScanAndSweep", "a retention rule, re-read per sweep"},
	"recording.minFreeGb":      {ClassLive, "CheckFreeSpace", "the free-space floor, re-read per sweep"},
	"recording.stems":          {ClassRespawn, "reconcileRecorder", "adds output mappings to the recorder's argv"},
	"recording.stemCodec":      {ClassRespawn, "reconcileRecorder", "the stem encoder in the argv"},

	// --------------------------------------------------------------- preview
	"preview.enabled":            {ClassRespawn, "reconcilePreview", "starts or stops the on-demand encoder"},
	"preview.segmentSeconds":     {ClassRespawn, "previewSig", "the HLS muxer's argv, and the keyframe expression derived from it"},
	"preview.videoHeight":        {ClassRespawn, "previewSig", "the scale filter"},
	"preview.videoKbps":          {ClassRespawn, "previewSig", "the encoder's rate control"},
	"preview.idleTimeoutSeconds": {ClassLive, "previewIdleWindow", "read by sweepPreview each tick and deliberately absent from previewSig, so changing it never cycles a live preview"},

	// --------------------------------------------------------------- playout
	"playout.enabled":              {ClassRespawn, "Reconcile", "starts or stops every variant"},
	"playout.public":               {ClassOnDemand, "playoutHandler", "evaluated per request, because a route table is built once at startup and this is a runtime setting"},
	"playout.sourceId":             {ClassOnDemand, "playoutManager", "which programme the public page serves, resolved per request -- no child restarts, the route simply asks a different engine"},
	"playout.allowCrossOrigin":     {ClassOnDemand, "setCORS", "a response header, decided per request"},
	"playout.format":               {ClassRespawn, "variantSig", "chooses the HLS or DASH muxer in the argv"},
	"playout.segmentSeconds":       {ClassRespawn, "variantSig", "-hls_time / -seg_duration"},
	"playout.playlistSegments":     {ClassRespawn, "variantSig", "-hls_list_size"},
	"playout.dvrWindowSeconds":     {ClassRespawn, "variantSig", "widens -hls_list_size"},
	"playout.maxDiskMb":            {ClassLive, "Sweep", "the sweeper reads m.settings, which Reconcile has already replaced"},
	"playout.audioKbps":            {ClassRespawn, "variantSig", "the AAC encoder's bitrate"},
	"playout.sessionIdleSeconds":   {ClassLive, "SetLimits", "a viewer-table bound, pushed in by Reconcile before any variant is touched"},
	"playout.maxSessions":          {ClassLive, "SetLimits", "a viewer-table bound"},
	"playout.variants.name":        {ClassRespawn, "variantSig", "names the variant's own output directory"},
	"playout.variants.enabled":     {ClassRespawn, "Reconcile", "starts or stops one muxer"},
	"playout.variants.renditionId": {ClassRespawn, "variantSig", "changes which relay the muxer reads"},
	"playout.variants.audioTrack":  {ClassRespawn, "variantSig", "a stream mapping"},

	// -------------------------------------------------------------- failover
	"failover.enabled":                              {ClassRespawn, "wantSelector", "starts or stops the selector tier, which moves every consumer's upstream signature"},
	"failover.graceSeconds":                         {ClassLive, "failoverGrace", "read by sweepSelector every 500ms"},
	"failover.return":                               {ClassLive, "applySourceChoice", "read by sweepSelector every 500ms"},
	"failover.returnStableSeconds":                  {ClassLive, "applySourceChoice", "read by sweepSelector every 500ms"},
	"failover.backup.enabled":                       {ClassRespawn, "reconcileBackupIngest", "starts or stops the second listener"},
	"failover.backup.mode":                          {ClassRespawn, "backupIngestSig", "the backup ingest's argv"},
	"failover.backup.srt.passphrase":                {ClassRespawn, "backupIngestSig", "an SRT socket option"},
	"failover.backup.srt.latencyMs":                 {ClassRespawn, "backupIngestSig", "an SRT socket option"},
	"failover.backup.rtmp.app":                      {ClassRespawn, "backupIngestSig", "part of the listener URL"},
	"failover.backup.rtmp.streamKey":                {ClassRespawn, "backupIngestSig", "matched at connect"},
	"failover.backup.pull.url":                      {ClassRespawn, "backupIngestSig", "the input FFmpeg dials"},
	"failover.backup.pull.reconnectDelayMaxSeconds": {ClassRespawn, "backupIngestSig", "an FFmpeg input option"},
	"failover.backup.pull.rtspTransport":            {ClassRespawn, "backupIngestSig", "an FFmpeg input option"},
	"failover.slate.enabled":                        {ClassLive, "applySourceChoice", "whether the slate is an eligible choice is re-read every 500ms; the slate's own argv is not"},
	"failover.slate.imagePath":                      {ClassRespawn, "feedUpstreamSig", "the input file in the slate feed's argv"},
	// Both were ClassLive/ClassRespawn-on-feedUpstreamSig when the playlist was
	// only a decision. It is a tier now -- a supervised process on a hub of its
	// own, exactly like the backup listener -- so enabling it spawns a child and
	// the file reaches that child's argv. Classified like the backup's two
	// equivalents above, and for the same reasons.
	"failover.playlist.enabled": {ClassRespawn, "reconcilePlaylist", "starts or stops the playlist tier; its eligibility as a choice is re-read every 500ms, but the process is not"},
	// filePath became items (DESIGN 2026-08-01-playlist-items): the playlist
	// is now an ordered list of uploads rather than one file, but the list
	// still reaches the tier's argv exactly as the path did, so the class and
	// the signature it moves are unchanged. Items is a slice of a struct, so
	// walkSettings descends into it and the leaf is the nested "upload" field,
	// not "items" itself.
	"failover.playlist.items.upload": {ClassRespawn, "playlistSig", "the input file in the playlist tier's argv"},

	// ----------------------------------------------------------------- synth
	"synth.silenceOnVideoOnly": {ClassRespawn, "wantSilence", "starts or stops the silence tier, which moves silenceSig and therefore every passthrough consumer"},

	// ---------------------------------------------------------------- meters
	"meters.enabled":    {ClassRespawn, "reconcileMeters", "starts or stops the sidecar"},
	"meters.intervalMs": {ClassLive, "applyMeterInterval", "a throttle in the Go stdout parser; it has never reached an argv, and capturing it at spawn made editing it a silent no-op"},

	// --------------------------------------------------------------- logging
	"logging.persistProcessLogs": {ClassLive, "applyLogging", "swaps the FileSink behind logSink, so children already running start filling the new file"},
	"logging.maxFileMb":          {ClassLive, "applyLogging", "re-opens the sink; no child is touched"},
	"logging.maxFiles":           {ClassLive, "applyLogging", "re-opens the sink; no child is touched"},

	// -------------------------------------------------------------- postProd
	"postProd.enabled":               {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.concurrency":           {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.defaultMode":           {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.yieldToStream":         {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.cpuCeilingPercent":     {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.cpuResumePercent":      {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.cpuSustainedSeconds":   {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.cpuSettleSeconds":      {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.avoidGpuWhenStreaming": {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.gpuBusy":               {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.batteryFloorPercent":   {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.thermalCeilingC":       {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.niceLevel":             {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.idleIo":                {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},
	"postProd.ingestLingerSeconds":   {ClassLive, "handlePutJobPolicy", "pushed into the governor by the jobs policy endpoint, which is where postProd is edited; the governor then consults it per admission in MayStart"},

	// ------------------------------------------------------------------ mqtt
	"mqtt.enabled":          {ClassLive, "ApplyAutomod", "the MQTT runner polls settings and notices by hash; see handlePutMQTTPassword"},
	"mqtt.brokerUrl":        {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.username":         {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.hasPassword":      {ClassLive, "ApplyAutomod", "reported by the runner, set through its own endpoint"},
	"mqtt.prefix":           {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.instance":         {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.clientId":         {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.intervalSeconds":  {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.keepAliveSeconds": {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.tlsSkipVerify":    {ClassLive, "ApplyAutomod", "polled by the runner"},
	"mqtt.discovery":        {ClassLive, "ApplyAutomod", "polled by the runner"},

	// ---------------------------------------------------------- destinations
	"destinations.staggerMs": {ClassLive, "startDestinations", "read per sweep and applied only to processes started in that sweep; it can never affect one already running"},

	// ---------------------------------------------------------- multitrack
	//
	// ON DEMAND, not respawn, and the distinction is real rather than a
	// classification convenience. None of these reaches an argv: they are the
	// body of a request made ONCE PER START, in startDest, and read out of
	// e.Settings() at that moment. A destination that is already publishing
	// negotiated its configuration at go-live and holds a minted key that
	// belongs to that negotiation -- changing the declared hardware underneath
	// it cannot retroactively change what Twitch agreed to, and restarting a
	// live destination to re-ask would drop a connection to a platform in order
	// to deliver a setting that only matters next time.
	//
	// So the honest answer is: it applies to the next start of each
	// destination, which is what ClassOnDemand says.
	"multitrack.gpus.model":                {ClassOnDemand, "negotiateFor", "read at go-live and sent as capabilities.gpu[].model"},
	"multitrack.gpus.vendorId":             {ClassOnDemand, "negotiateFor", "read at go-live; the field Twitch validates, and zero is what it refuses by name"},
	"multitrack.gpus.deviceId":             {ClassOnDemand, "negotiateFor", "read at go-live and sent as capabilities.gpu[].device_id"},
	"multitrack.gpus.dedicatedVideoMemory": {ClassOnDemand, "negotiateFor", "read at go-live and sent as capabilities.gpu[].dedicated_video_memory"},
	"multitrack.gpus.sharedSystemMemory":   {ClassOnDemand, "negotiateFor", "read at go-live and sent as capabilities.gpu[].shared_system_memory"},
	"multitrack.gpus.driverVersion":        {ClassOnDemand, "negotiateFor", "read at go-live; Twitch refuses an out-of-date driver naming the version to upgrade to"},

	// ------------------------------------------------------------ chat + automod
	"chat.retentionHours": {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band by handlePutSettings"},
	"chat.keepMessages":   {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band"},
	"chat.purgeMinutes":   {ClassLive, "ApplyChatRetention", "pushed into the Hub out of band"},

	"automod.enabled":              {ClassLive, "ApplyAutomod", "rebuilds the automod engine out of band"},
	"automod.platformEnabled":      {ClassLive, "ApplyAutomod", "rebuilds the automod engine"},
	"automod.on":                   {ClassLive, "ApplyAutomod", "rebuilds the matrix"},
	"automod.rules.id":             {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.name":           {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.enabled":        {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.pattern":        {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.action":         {ClassLive, "ApplyAutomod", "recompiles the patterns"},
	"automod.rules.timeoutSeconds": {ClassLive, "ApplyAutomod", "recompiles the patterns"},

	// ------------------------------------------------- ingest track annotations
	// Each leaf recompiles every routing graph, so it moves compiled.FilterComplex
	// and therefore destSpec.
	"ingest.annotations.track":    {ClassRespawn, "planDestinations", "recompiles every routing graph, moving destSpec"},
	"ingest.annotations.label":    {ClassRespawn, "planDestinations", "recompiles every routing graph, moving destSpec"},
	"ingest.annotations.role":     {ClassRespawn, "planDestinations", "recompiles every routing graph, moving destSpec"},
	"ingest.annotations.language": {ClassRespawn, "planDestinations", "recompiles every routing graph, moving destSpec"},
	"ingest.annotations.denoise":  {ClassRespawn, "planDestinations", "adds a filter to the graph, moving destSpec"},

	// --------------------------------------------------------- the slate's argv
	"failover.slate.color":     {ClassRespawn, "feedUpstreamSig", "the generated picture's colour, fixed in the slate feed's argv"},
	"failover.slate.videoKbps": {ClassRespawn, "feedUpstreamSig", "the slate encoder's rate control"},
	"failover.slate.encoder":   {ClassRespawn, "feedUpstreamSig", "chooses the encoder in the argv"},
	"failover.slate.preset":    {ClassRespawn, "feedUpstreamSig", "an encoder argument"},

	// ------------------------------------------------------ post-production tail
	"postProd.deferSeconds": {ClassLive, "handlePutJobPolicy", "how far ahead a blocked job is parked; read by the governor per decision"},
	"postProd.whisperModel": {ClassLive, "handlePutJobPolicy", "chosen per transcribe job at admission, not held by a running process"},
	// Retention is the one honest exception in this file. See ClassNextStart.
	"postProd.retainDays": {ClassLive, "purgeJobHistoryLoop", "re-read every sweep through the settings func the loop was handed, the same shape recording retention uses"},
	"postProd.retainJobs": {ClassLive, "purgeJobHistoryLoop", "re-read every sweep; the floor that survives whatever their age"},

	"postProd.kinds.kind":         {ClassLive, "handlePutJobPolicy", "per-kind policy, consulted by the governor per admission"},
	"postProd.kinds.mode":         {ClassLive, "handlePutJobPolicy", "per-kind policy, consulted per admission"},
	"postProd.kinds.usesGpu":      {ClassLive, "handlePutJobPolicy", "per-kind policy, consulted per admission"},
	"postProd.kinds.ignoreIngest": {ClassLive, "handlePutJobPolicy", "per-kind policy, consulted per admission"},

	"postProd.kinds.windows.days":         {ClassLive, "handlePutJobPolicy", "a scheduling window, evaluated per admission against the clock"},
	"postProd.kinds.windows.startMinutes": {ClassLive, "handlePutJobPolicy", "evaluated per admission"},
	"postProd.kinds.windows.endMinutes":   {ClassLive, "handlePutJobPolicy", "evaluated per admission"},
	"postProd.kinds.windows.tz":           {ClassLive, "handlePutJobPolicy", "evaluated per admission"},

	// ------------------------------------------------------------- chat + alerts
	"chat.historyMessages": {ClassLive, "ApplyChatRetention", "resizes the in-memory ring on the running Hub via SetHistory"},
	"alerts.retryAttempts": {ClassLive, "ApplyAlertSettings", "pushed into every engine's Notifier, and remembered for engines created later"},

	// ------------------------------------------------------- automod: history
	"automod.history.maxMessages":           {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.windowSeconds":         {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.maxRepeats":            {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.maxLinks":              {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.maxMentionsPerMessage": {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.maxCapsRatio":          {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.minLengthForCaps":      {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.retainPerAuthor":       {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.idleEvictionSeconds":   {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.action":                {ClassLive, "ApplyAutomod", "rebuilds the history checker"},
	"automod.history.timeoutSeconds":        {ClassLive, "ApplyAutomod", "rebuilds the history checker"},

	// --------------------------------------------------------- automod: model
	"automod.model.enabled":         {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.endpoint":        {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.model":           {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.hasApiKey":       {ClassLive, "ApplyAutomod", "reported by the checker; the key itself is sealed and set through its own endpoint"},
	"automod.model.instruction":     {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.minConfidence":   {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.maxCallsPerHour": {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.timeoutSeconds":  {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.action":          {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
	"automod.model.timeoutForBan":   {ClassLive, "ApplyAutomod", "rebuilds the model checker"},
}

// ------------------------------------------------------------ what a reconcile did

const (
	// reloadRestart means a child process was replaced to apply the change.
	reloadRestart = "restart"
	// reloadLive means the change reached a process that kept running.
	reloadLive = "live"
	// reloadStop means a tier was left with no process at all, and the note is
	// why. A third value rather than reporting a refusal as a "restart",
	// because the two are opposite facts about whether anything is running and
	// an operator reading "restart" would go looking for a child that is not
	// there. #255's refused pull upload is the first: the reconcile is where
	// that refusal finally has somewhere to be reported.
	reloadStop = "stop"

	// eventReload announces what a reconcile moved. Declared here rather than
	// in internal/events because it is only meaningful to a system that has a
	// reconciler; the broker takes any type. Same precedent as eventFailover.
	eventReload events.Type = "reload"
)

// ReloadNote is one thing a reconcile did.
type ReloadNote struct {
	Tier   string `json:"tier"`
	Name   string `json:"name"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// ReloadReport is everything one engine's reconcile did.
type ReloadReport struct {
	SourceID   int64        `json:"sourceId"`
	SourceName string       `json:"sourceName"`
	Notes      []ReloadNote `json:"notes"`
}

// reloadRecorder collects notes for one reconcile.
//
// It carries its own mutex rather than riding on e.mu because notes are raised
// from teardown paths that already hold, or are about to take, e.mu -- and
// because a note must never be the thing that deadlocks a reconcile.
type reloadRecorder struct {
	mu    sync.Mutex
	notes []ReloadNote
}

func newReloadRecorder() *reloadRecorder { return &reloadRecorder{} }

func (r *reloadRecorder) note(tier, name, action, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, ReloadNote{Tier: tier, Name: name, Action: action, Reason: reason})
}

func (r *reloadRecorder) snapshot() []ReloadNote {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReloadNote(nil), r.notes...)
}

// noteReload records something this reconcile did, if a reconcile is what is
// doing it.
//
// A nil recorder is the normal case outside Reconcile and the note is dropped
// on purpose: the preview idling out and the storage guard halting the recorder
// are real events, but they are not consequences of anything the operator just
// saved, and folding them into a settings response would tell somebody their
// edit stopped a recording it had nothing to do with. They already reach the
// operator as TypeStatus and as alerts.
func (e *Engine) noteReload(tier, name, action, reason string) {
	if r := e.reloadRec.Load(); r != nil {
		r.note(tier, name, action, reason)
	}
}

// LastReload is what the most recent reconcile did.
//
// Honest limitation: concurrent reconciles interleave into one recorder, so two
// handlers saving at the same moment each see the union. That is the truth
// about what moved, which is more useful than a per-caller fiction, but it is
// not a per-request audit log and must not be read as one.
func (e *Engine) LastReload() ReloadReport {
	rep := e.lastReload.Load()
	if rep == nil {
		return ReloadReport{SourceID: e.sourceID, SourceName: e.SourceName()}
	}
	return *rep
}
