import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useCopy } from "@/hooks/useCopy";
import Hls from "hls.js";
import { api, ApiError } from "@/lib/api";
import type { BeaconKind, PlayoutPublicView } from "@/lib/types";
import { useT } from "@/lib/i18n";
import { type ClockAnchor, useLiveCaptionTrack } from "@/features/player/liveCaptions";
import {
  browserCodecEnv,
  type CodecSupport,
  detectCodecSupport,
  forSegments,
  nativeSource,
  playableVariants,
} from "@/features/player/playerCodecs";
import { countdownUnit, mediaTimeFor, parsePlayerLink, shareLinkAt, wallTimeAt } from "@/features/player/playerLink";
import { classifyHlsError, classifyMediaError, HEARTBEAT_MS, QoETracker } from "@/features/player/qoeBeacon";
import {
  hasDvrWindow,
  isBehindLive,
  levelsToRemove,
  type PlayRange,
  type QualityOption,
  qualityOptions,
} from "@/features/player/playerControls";

/** The public player page, served at /watch and /watch/:token.
 *
 *  Three constraints shape this file, and they are why it shares almost nothing
 *  with the admin UI:
 *
 *   - It must work with NO SESSION. Nothing here calls an authenticated
 *     endpoint, reads the CSRF cookie, or opens the telemetry socket.
 *   - It must stay LIGHT. A viewer following a shared link should not download
 *     the console. No shadcn primitives, no lucide icons, no sonner, no
 *     recharts — plain elements and Tailwind classes, so the only meaningful
 *     weight in this chunk is hls.js, which is the thing that plays the video.
 *   - It must be EMBEDDABLE. It renders inside an iframe on somebody else's
 *     site, at whatever size they chose, so the layout is fluid and the page
 *     never assumes it owns the viewport.
 *
 *  It is also deliberately styled without the app's design tokens: an embed on
 *  an unknown page cannot rely on the admin theme's CSS variables being loaded
 *  in a predictable state, so the colours here are literal. */

/** There is no "stream exists but is off" state at this level, and that is
 *  deliberate: the server answers 404 for a stream that is not published, so
 *  that an anonymous caller cannot tell a private stream from an address with
 *  nothing behind it. `waiting` is that 404, phrased for the far more likely
 *  cause — a viewer who opened the link before the operator went live. A
 *  PUBLISHED stream that is not on air is `ready` with `view.state` offline or
 *  ended, and gets the offline card. */
type Phase =
  | { kind: "loading" }
  | { kind: "denied" }
  | { kind: "waiting" }
  | { kind: "ready"; view: PlayoutPublicView };

export function PublicPlayer() {
  const t = useT();
  const [link] = useState(() => parsePlayerLink(window.location.pathname, window.location.search));
  const token = link.token;
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [playing, setPlaying] = useState(false);

  const load = useCallback(async () => {
    try {
      const view = await api.playoutPublic(token);
      setPhase({ kind: "ready", view });
    } catch (err) {
      // A wrong token is the one failure worth naming, because it is the one
      // the viewer can do something about — ask for a fresh link.
      setPhase({
        kind: err instanceof ApiError && err.status === 401 ? "denied" : "waiting",
      });
    }
  }, [token]);

  useEffect(() => {
    void load();
  }, [load]);

  // Keep looking while there is nothing to play, so a viewer who opened the
  // link early starts watching when the broadcast does rather than having to
  // know to refresh. Only for `waiting`: a bad token will not fix itself, and
  // polling it would be a login attempt every fifteen seconds forever.
  useEffect(() => {
    if (phase.kind !== "waiting") return;
    const id = window.setInterval(() => void load(), 15000);
    return () => window.clearInterval(id);
  }, [phase.kind, load]);

  // And keep asking once there IS something to show, because the view is
  // where `viewers` and `state` come from: the offline card turns into the
  // player when the broadcast starts, and the count keeps moving.
  //
  // Deliberately NOT `load()`: this refresh may never demote the phase. A
  // transient 404 or a blip would otherwise tear the player down and drop a
  // viewer who is watching perfectly well. On an error it keeps what it has.
  useEffect(() => {
    if (phase.kind !== "ready") return;
    const id = window.setInterval(() => {
      api
        .playoutPublic(token)
        .then((view) => setPhase({ kind: "ready", view }))
        .catch(() => {});
    }, 15000);
    return () => window.clearInterval(id);
  }, [phase.kind, token]);

  if (phase.kind === "loading") {
    return (
      <Shell>
        <p className="text-sm text-neutral-400">{t("player.loading")}</p>
      </Shell>
    );
  }

  if (phase.kind === "denied") {
    return (
      <Shell>
        <h1 className="text-base font-semibold text-neutral-100">
          {t("player.privateTitle")}
        </h1>
        <p className="mt-2 max-w-sm text-sm text-neutral-400">
          {t("player.missingToken")}
        </p>
      </Shell>
    );
  }

  if (phase.kind === "waiting") {
    return (
      <Shell>
        <h1 className="text-base font-semibold text-neutral-100">{t("player.notLive")}</h1>
        <p className="mt-2 max-w-sm text-sm text-neutral-400">
          {t("player.willStart")}
        </p>
      </Shell>
    );
  }

  const { view } = phase;
  // A server from before `state` existed is read as live, which is what the
  // page always assumed. And a video that is PLAYING stays up whatever a poll
  // says: the last segments are still in the buffer, and cutting a viewer off
  // mid-sentence because the poll landed first would be the wrong way round.
  const live = (view.state ?? "live") === "live" || playing;

  return (
    <div className="min-h-dvh bg-neutral-950 text-neutral-100">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4">
        {live ? (
          <LivePlayer view={view} token={token} atMs={link.atMs} onPlayingChange={setPlaying} />
        ) : (
          <OfflineCard view={view} token={token} />
        )}
        <About view={view} live={live} />
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ the player

function LivePlayer({
  view,
  token,
  atMs,
  onPlayingChange,
}: Readonly<{
  view: PlayoutPublicView;
  token?: string;
  atMs?: number;
  onPlayingChange: (playing: boolean) => void;
}>) {
  const t = useT();
  const [playing, setPlayingState] = useState(false);
  const setPlaying = useCallback(
    (p: boolean) => {
      setPlayingState(p);
      onPlayingChange(p);
    },
    [onPlayingChange],
  );
  const videoRef = useRef<HTMLVideoElement | null>(null);
  // The element as state as well, so the caption track hook runs once it exists.
  const [videoEl, setVideoEl] = useState<HTMLVideoElement | null>(null);
  const attachVideo = useCallback((el: HTMLVideoElement | null) => {
    videoRef.current = el;
    setVideoEl(el);
  }, []);
  const hlsRef = useRef<Hls | null>(null);

  // Where the playhead is on the wall clock, from the newest fragment's
  // EXT-X-PROGRAM-DATE-TIME. The live captions, the share-this-moment link and
  // a ?t= start are all placed through it.
  const clockRef = useRef<ClockAnchor | null>(null);
  const captionClock = useCallback((): ClockAnchor | null => {
    if (clockRef.current) return clockRef.current;
    // Safari's native HLS exposes the playlist's date as the start date of
    // media time 0 instead of handing out fragments.
    const v = videoRef.current as (HTMLVideoElement & { getStartDate?: () => Date }) | null;
    const d = v?.getStartDate?.();
    if (d && !Number.isNaN(d.getTime())) return { wallMs: d.getTime(), mediaTime: 0 };
    return null;
  }, []);

  const [quality, setQuality] = useState<QualityOption[]>([]);
  const [level, setLevel] = useState(-1);
  const [fellBack, setFellBack] = useState(false);
  const [range, setRange] = useState<PlayRange | null>(null);
  const [shareLink, setShareLink] = useState("");
  // A blocked clipboard (cross-origin iframe, plain HTTP) is not reported: the link is
  // shown in a field below for copying by hand.
  const { copy: copyShare, copied: shareCopied } = useCopy(2000);
  const [momentGone, setMomentGone] = useState(false);
  const pendingSeek = useRef<number | undefined>(atMs);

  const master = view.master;
  const variants = useMemo(() => view.variants ?? [], [view.variants]);
  // Variant NAMES as the player's key: the view is re-read every fifteen
  // seconds, and a new array with the same rungs must not rebuild the player.
  // Codecs are deliberately not in it -- the source rung's codec arrives with
  // the ingest probe, after viewers are already watching, and rebuilding every
  // viewer's player to learn it would interrupt them all. A codec that becomes
  // known is applied to the running player instead; see codecKey below.
  const ladderKey = variants.map((v) => v.name).join(",");
  const codecKey = variants.map((v) => `${v.name}:${v.codec ?? ""}`).join(",");
  const trackerRef = useRef<QoETracker | null>(null);
  const variantsRef = useRef(variants);
  variantsRef.current = variants;
  // What this browser decodes, read once per player.
  const supportRef = useRef<CodecSupport | null>(null);
  const fellBackReported = useRef(false);
  const nativeSrcRef = useRef("");

  /** Take rungs this browser cannot play out of a running hls.js, from the
   *  ladder as the server describes it NOW. Called when the manifest is
   *  parsed and again whenever a rung's codec becomes known. */
  const applyLadder = useCallback((h: Hls) => {
    const support = supportRef.current;
    if (!support || h.levels.length === 0) return;
    const ladder = playableVariants(variantsRef.current, forSegments(support, "mse"));
    for (const i of levelsToRemove(h.levels, new Set(ladder.dropped.map((v) => v.name)))) h.removeLevel(i);
    if (ladder.dropped.length > 0) {
      setFellBack(true);
      if (!fellBackReported.current) {
        fellBackReported.current = true;
        trackerRef.current?.error("codec_unsupported");
      }
    }
    setQuality(qualityOptions(h.levels));
  }, []);

  // The live caption sidecar sits beside the master playlist. It is fetched
  // under /playout/, so the playback cookie the manifest request earned covers it.
  const captionsUrl = master ? new URL("captions.vtt", new URL(master, window.location.href)).pathname : "";
  useLiveCaptionTrack(videoEl, captionsUrl, t("wave3.captions.playerTrack"), captionClock);

  // ---- QoE: one tracker per mounted player, beacons on a heartbeat and on
  // exit. Created by the effect that owns its lifetime rather than in render,
  // so a remount (React's development double-mount, or the page flipping to
  // the offline card and back) starts a fresh session instead of reusing one
  // that has already sent `end`.
  const sendBeacon = useCallback((kind: BeaconKind) => {
    const tracker = trackerRef.current;
    if (!tracker) return;
    const v = videoRef.current;
    const hls = hlsRef.current;
    if (hls) {
      tracker.setLatency(hls.latency);
      const lv = hls.levels[hls.currentLevel >= 0 ? hls.currentLevel : hls.loadLevel];
      tracker.setVariant(lv?.name);
    } else if (v && v.seekable.length > 0) {
      tracker.setLatency(v.seekable.end(v.seekable.length - 1) - v.currentTime);
    }
    tracker.beacon(kind);
  }, []);
  useEffect(() => {
    trackerRef.current = new QoETracker({ send: (b) => void api.playoutBeacon(b, token) });
    const id = window.setInterval(() => sendBeacon("heartbeat"), HEARTBEAT_MS);
    const bye = () => sendBeacon("end");
    window.addEventListener("pagehide", bye);
    return () => {
      window.clearInterval(id);
      window.removeEventListener("pagehide", bye);
      bye();
      trackerRef.current = null;
    };
  }, [sendBeacon, token]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video || !master) return;

    // The token rides on the manifest URL. Every request after that one is
    // authorised by the cookie the server set in reply, which is what lets a
    // playlist of relative segment URLs work at all.
    const q = token ? `?t=${encodeURIComponent(token)}` : "";
    const support = detectCodecSupport(browserCodecEnv(video));
    supportRef.current = support;
    let hls: Hls | null = null;
    let retry: number | undefined;
    clockRef.current = null;

    const start = () => {
      // Startup is measured from the attempt that played, not from the page
      // load: a viewer who opened the link before the broadcast began has not
      // been waiting on the player for that time (#5).
      trackerRef.current?.reset();
      if (Hls.isSupported()) {
        hls = new Hls({
          // Conservative next to the dashboard's monitor feed. A viewer wants
          // an unbroken picture far more than they want two seconds less
          // latency, and an embed may be on a much worse connection than the
          // operator's LAN.
          lowLatencyMode: false,
          maxBufferLength: 30,
          manifestLoadingMaxRetry: 4,
          levelLoadingMaxRetry: 4,
        });
        hlsRef.current = hls;
        // withCredentials is deliberately left alone. The media is same-origin
        // with this page — an embed loads /watch from this server, so its own
        // requests are same-origin too — and same-origin requests carry the
        // playout cookie regardless. Setting it would only matter cross-origin,
        // where it would collide with the wildcard CORS header the media sends
        // and break playback outright.
        hls.loadSource(master + q);
        hls.attachMedia(video);
        hls.on(Hls.Events.MANIFEST_PARSED, () => {
          // HEVC/AV1 fallback. hls.js filters on CODECS itself where the
          // master carries them; this is the same decision made from the
          // server's own codec field and from what MPEG-TS segments allow,
          // so a rung the browser cannot play is never offered or chosen.
          if (hls) applyLadder(hls);
        });
        hls.on(Hls.Events.LEVELS_UPDATED, () => hls && setQuality(qualityOptions(hls.levels)));
        hls.on(Hls.Events.FRAG_CHANGED, (_e, data) => {
          const pdt = data.frag.programDateTime;
          if (pdt) clockRef.current = { wallMs: pdt, mediaTime: data.frag.start };
        });
        hls.on(Hls.Events.ERROR, (_e, data) => {
          if (!data.fatal) return;
          trackerRef.current?.error(classifyHlsError({ type: data.type, details: data.details }));
          setPlaying(false);
          hls?.destroy();
          hls = null;
          hlsRef.current = null;
          // A stream that has not started yet has no playlist, which is a
          // normal state rather than an error. Keep looking.
          retry = window.setTimeout(start, 5000);
        });
      } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
        // Safari and most set-top boxes play HLS natively. They cannot set a
        // header, which is the whole reason the token is accepted in the query
        // string and traded for a cookie. Native playback cannot be told to
        // skip a rung, so when the ladder holds one this browser cannot
        // decode, it loads the best H.264 rung's own playlist instead.
        const src = nativeSource(master, variantsRef.current, forSegments(support, "native"));
        if (src !== master) setFellBack(true);
        nativeSrcRef.current = src;
        video.src = src + q;
      }
    };

    start();
    return () => {
      if (retry) window.clearTimeout(retry);
      hls?.destroy();
      hlsRef.current = null;
    };
    // ladderKey stands for `variants`; see its comment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [master, token, ladderKey, setPlaying, applyLadder]);

  // A rung's codec became known after the player started (#8). hls.js drops
  // the rung in place; native HLS, which cannot be told to skip one, is moved
  // to the H.264 rung only if that is now where it should be.
  useEffect(() => {
    const h = hlsRef.current;
    if (h) {
      applyLadder(h);
      return;
    }
    const video = videoRef.current;
    const support = supportRef.current;
    if (!video || !support || !nativeSrcRef.current) return;
    const src = nativeSource(master, variantsRef.current, forSegments(support, "native"));
    if (src !== nativeSrcRef.current) {
      nativeSrcRef.current = src;
      setFellBack(src !== master);
      video.src = src + (token ? `?t=${encodeURIComponent(token)}` : "");
    }
    // codecKey is what this effect is about; the refs carry the rest.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [codecKey]);

  // The DVR bar's model, refreshed from the element as it plays.
  const refreshRange = useCallback(() => {
    const v = videoRef.current;
    if (!v || v.seekable.length === 0) {
      setRange(null);
      return;
    }
    const start = v.seekable.start(0);
    const end = v.seekable.end(v.seekable.length - 1);
    const edge = hlsRef.current?.liveSyncPosition ?? end;
    setRange({ start, end, at: v.currentTime, edge });

    // A ?t= start, applied once the stream can say where that moment is.
    if (pendingSeek.current !== undefined) {
      const anchor = captionClock();
      if (anchor) {
        const target = mediaTimeFor(pendingSeek.current, anchor, { start, end });
        pendingSeek.current = undefined;
        if (target === undefined) setMomentGone(true);
        else v.currentTime = target;
      }
    }
  }, [captionClock]);

  const goLive = () => {
    const v = videoRef.current;
    if (!v || !range) return;
    v.currentTime = range.edge;
    void v.play().catch(() => {});
  };

  const shareMoment = async () => {
    const v = videoRef.current;
    const anchor = captionClock();
    if (!v || !anchor) return;
    const link = shareLinkAt(window.location.origin, token, wallTimeAt(v.currentTime, anchor));
    setShareLink(link);
    await copyShare(link);
  };

  const chooseLevel = (idx: number) => {
    setLevel(idx);
    if (hlsRef.current) hlsRef.current.currentLevel = idx;
  };

  const behind = isBehindLive(range);
  const hasDvr = hasDvrWindow(range);
  const poster = api.playoutPosterUrl(token);

  return (
    <div className="flex flex-col gap-2">
      <div className="relative w-full overflow-hidden rounded-lg bg-black shadow-lg">
        <div className="aspect-video w-full">
          <video
            ref={attachVideo}
            className="h-full w-full"
            poster={poster || undefined}
            controls
            playsInline
            autoPlay
            muted
            onPlaying={() => {
              const tracker = trackerRef.current;
              const first = !!tracker && !tracker.started;
              tracker?.playing();
              setPlaying(true);
              if (first) sendBeacon("start");
            }}
            onWaiting={() => {
              trackerRef.current?.waiting();
              setPlaying(false);
            }}
            onSeeking={() => trackerRef.current?.seekStarted()}
            onSeeked={() => trackerRef.current?.seekEnded()}
            onError={(e) => trackerRef.current?.error(classifyMediaError(e.currentTarget.error?.code))}
            onTimeUpdate={refreshRange}
            onLoadedMetadata={refreshRange}
          />
        </div>

        {!playing && (
          <div className="pointer-events-none absolute left-3 top-3 rounded bg-black/70 px-2 py-1 text-[11px] text-neutral-300">
            {t("player.connecting")}
          </div>
        )}

        {playing && (
          <div
            className={
              "pointer-events-none absolute left-3 top-3 flex items-center gap-1.5 rounded bg-black/70 px-2 py-1 text-[11px] font-medium " +
              (behind ? "text-neutral-300" : "text-red-400")
            }
          >
            <span className={"h-1.5 w-1.5 rounded-full " + (behind ? "bg-neutral-500" : "bg-red-500")} />
            {behind ? t("player.behindLive") : t("player.liveBadge")}
          </div>
        )}
      </div>

      {/* The toolbar sits below the video rather than fighting the native
          controls, which differ per browser and cannot be extended. */}
      <div className="flex flex-wrap items-center gap-2 text-xs text-neutral-400">
        {hasDvr && range && (
          <input
            type="range"
            aria-label={t("player.seekBar")}
            className="min-w-[8rem] flex-1 accent-red-500"
            min={range.start}
            max={range.end}
            step={1}
            value={Math.min(Math.max(range.at, range.start), range.end)}
            onChange={(e) => {
              const v = videoRef.current;
              if (v) v.currentTime = Number(e.currentTarget.value);
            }}
          />
        )}
        {behind && (
          <button
            type="button"
            onClick={goLive}
            className="rounded bg-red-600 px-2.5 py-1 font-medium text-white hover:bg-red-500"
          >
            {t("player.goLive")}
          </button>
        )}
        {quality.length > 1 && (
          <label className="flex items-center gap-1.5">
            <span>{t("player.quality")}</span>
            <select
              value={level}
              onChange={(e) => chooseLevel(Number(e.currentTarget.value))}
              className="rounded border border-neutral-800 bg-neutral-900 px-1.5 py-1 text-neutral-200"
            >
              <option value={-1}>{t("player.qualityAuto")}</option>
              {quality.map((q) => (
                <option key={q.index} value={q.index}>
                  {q.label}
                </option>
              ))}
            </select>
          </label>
        )}
        <button
          type="button"
          onClick={() => void shareMoment()}
          className="rounded bg-neutral-800 px-2.5 py-1 text-neutral-200 hover:bg-neutral-700"
        >
          {shareCopied ? t("player.shareCopied") : t("player.shareMoment")}
        </button>
      </div>
      {shareLink && (
        <input
          readOnly
          aria-label={t("player.shareMoment")}
          value={shareLink}
          onFocus={(e) => e.currentTarget.select()}
          className="w-full rounded border border-neutral-800 bg-neutral-950 p-1.5 font-mono text-[11px] text-neutral-300"
        />
      )}
      {momentGone && <p className="text-xs text-amber-400">{t("player.momentGone")}</p>}
      {fellBack && <p className="text-xs text-neutral-500">{t("player.codecFallback")}</p>}
    </div>
  );
}

// --------------------------------------------------------------- offline card

/** What a published stream shows when nothing is on air: the poster, whether
 *  a broadcast just ended or nothing is on yet, a countdown to the next
 *  scheduled start, and whatever the operator put on the card -- a message, a
 *  looping clip, a link to the latest VOD. */
export function OfflineCard({
  view,
  token,
  now: nowProp,
}: Readonly<{
  view: PlayoutPublicView;
  token?: string;
  /** For tests: a fixed clock instead of a ticking one. */
  now?: number;
}>) {
  const t = useT();
  const [tick, setTick] = useState(() => Date.now());
  const ticking = nowProp === undefined && !!view.nextStartAt;
  useEffect(() => {
    if (!ticking) return;
    const id = window.setInterval(() => setTick(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [ticking]);
  const now = nowProp ?? tick;

  const ended = view.state === "ended";
  const next = view.nextStartAt ? Date.parse(view.nextStartAt) : Number.NaN;
  const until = Number.isNaN(next) ? undefined : countdownUnit(next, now);
  const offline = view.offline ?? {};
  const clip = offline.clip ? api.playoutOfflineClipUrl(offline.clip, token) : "";
  const poster = api.playoutPosterUrl(token);

  const renderNextStart = () => {
    if (until) {
      return (
        <p className="text-sm text-neutral-100" data-testid="countdown">
          {t("player.nextIn", { countdown: relative(until) })}
          <span className="block text-xs text-neutral-400">
            {t("player.nextAt", { time: formatWhen(view.nextStartAt!) })}
          </span>
        </p>
      );
    }
    if (next && !Number.isNaN(next)) {
      return <p className="text-sm text-neutral-100">{t("player.startingSoon")}</p>;
    }
    return !ended && <p className="max-w-sm text-sm text-neutral-300">{t("player.willStart")}</p>;
  };

  return (
    <div
      className="relative w-full overflow-hidden rounded-lg bg-neutral-900 shadow-lg"
      data-testid="offline-card"
      data-state={ended ? "ended" : "offline"}
    >
      <div className="relative aspect-video w-full">
        {clip ? (
          <video
            className="absolute inset-0 h-full w-full object-cover opacity-60"
            src={clip}
            poster={poster}
            autoPlay
            muted
            loop
            playsInline
            tabIndex={-1}
            aria-hidden="true"
          />
        ) : (
          <img
            className="absolute inset-0 h-full w-full object-cover opacity-40"
            src={poster}
            alt=""
            onError={(e) => {
              // A stream that has never been live has no frame to show; the
              // card stands on its own without one.
              e.currentTarget.style.display = "none";
            }}
          />
        )}
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/55 p-6 text-center">
          <span className="rounded bg-black/60 px-2 py-0.5 text-[11px] font-medium uppercase tracking-wide text-neutral-300">
            {ended ? t("player.endedBadge") : t("player.offlineBadge")}
          </span>
          <h1 className="text-lg font-semibold text-neutral-50">
            {ended ? t("player.ended") : t("player.notLive")}
          </h1>
          {ended && view.endedAt && (
            <p className="text-xs text-neutral-300">
              {t("player.endedAt", { time: formatWhen(view.endedAt) })}
            </p>
          )}
          {renderNextStart()}
          {offline.message && (
            <p className="max-w-md whitespace-pre-wrap text-sm text-neutral-200">{offline.message}</p>
          )}
          {offline.vodUrl && (
            <a
              href={offline.vodUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-1 rounded bg-neutral-100 px-3 py-1.5 text-xs font-medium text-neutral-900 hover:bg-white"
            >
              {t("player.watchVod")}
            </a>
          )}
        </div>
      </div>
    </div>
  );
}

function relative(u: { value: number; unit: Intl.RelativeTimeFormatUnit }): string {
  try {
    return new Intl.RelativeTimeFormat(document.documentElement.lang || undefined, { numeric: "auto" }).format(
      u.value,
      u.unit,
    );
  } catch {
    return `${u.value} ${u.unit}`;
  }
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(document.documentElement.lang || undefined, {
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

// --------------------------------------------------------------------- about

function About({ view, live }: Readonly<{ view: PlayoutPublicView; live: boolean }>) {
  const t = useT();
  // The textarea below is always present, so a blocked clipboard (cross-origin iframe,
  // plain HTTP) is not reported: the snippet is still selectable by hand.
  const { copy, copied } = useCopy(2000);
  const embedSnippet =
    `<iframe src="${window.location.origin}${window.location.pathname}" ` +
    `width="854" height="480" frameborder="0" ` +
    `allow="autoplay; fullscreen; picture-in-picture" allowfullscreen></iframe>`;

  const copyEmbed = async () => {
    await copy(embedSnippet);
  };

  return (
    <>
      {(view.title || view.description) && (
        <div>
          {view.title && <h2 className="text-lg font-semibold tracking-tight">{view.title}</h2>}
          {view.description && (
            <p className="mt-1 whitespace-pre-wrap text-sm text-neutral-400">{view.description}</p>
          )}
        </div>
      )}

      {live && (
        <div className="flex flex-wrap items-center gap-3 text-xs text-neutral-500">
          <span>
            {view.viewers} {view.viewers === 1 ? t("player.viewer") : t("player.viewers")}
          </span>
        </div>
      )}

      {/* Hidden inside an embed: nobody wants an embed snippet inside an
          embed, and the nested copy would carry the wrong URL anyway. */}
      {window.self === window.top && (
        <details className="rounded-lg border border-neutral-800 bg-neutral-900/60 p-3">
          <summary className="cursor-pointer text-xs text-neutral-400">{t("player.embed")}</summary>
          <textarea
            readOnly
            value={embedSnippet}
            onFocus={(e) => e.currentTarget.select()}
            className="mt-2 h-20 w-full resize-none rounded border border-neutral-800 bg-neutral-950 p-2 font-mono text-[11px] text-neutral-300"
          />
          <button
            type="button"
            onClick={() => void copyEmbed()}
            className="mt-2 rounded bg-neutral-800 px-2.5 py-1 text-[11px] text-neutral-200 hover:bg-neutral-700"
          >
            {copied ? t("clipedit.copied") : t("common.copy")}
          </button>
        </details>
      )}
    </>
  );
}

/** Centred frame for the states that have no video to show. */
function Shell({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-neutral-950 p-6 text-center text-neutral-100">
      {children}
    </div>
  );
}
