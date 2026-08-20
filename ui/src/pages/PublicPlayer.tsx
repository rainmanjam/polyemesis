import { useCallback, useEffect, useRef, useState } from "react";
import Hls from "hls.js";
import { api, ApiError } from "@/lib/api";
import type { PlayoutPublicView } from "@/lib/types";
import { useT } from "@/lib/i18n";

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

/** Read the playback token out of /watch/:token, falling back to ?t=.
 *
 *  The path form is what an operator shares, because a URL with a path segment
 *  survives being pasted into places that mangle query strings. The query form
 *  is accepted too, since that is what the media URLs themselves carry. */
function tokenFromLocation(): string | undefined {
  let path = window.location.pathname;
  while (path.endsWith("/")) path = path.slice(0, -1);
  const prefix = "/watch/";
  if (path.startsWith(prefix)) {
    const raw = path.slice(prefix.length);
    if (raw) return decodeURIComponent(raw);
  }
  const q = new URLSearchParams(window.location.search).get("t");
  return q ?? undefined;
}

/** There is no "stream exists but is off" state, and that is deliberate: the
 *  server answers 404 for a stream that is not published, so that an anonymous
 *  caller cannot tell a private stream from an address with nothing behind it.
 *  `waiting` is that 404, phrased for the far more likely cause — a viewer who
 *  opened the link before the operator went live. */
type Phase =
  | { kind: "loading" }
  | { kind: "denied" }
  | { kind: "waiting" }
  | { kind: "ready"; view: PlayoutPublicView };

export function PublicPlayer() {
  const t = useT();
  const [token] = useState<string | undefined>(tokenFromLocation);
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const [playing, setPlaying] = useState(false);
  const [copied, setCopied] = useState(false);
  const videoRef = useRef<HTMLVideoElement>(null);

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

  // And keep asking once there IS something to play, because the view is also
  // where `viewers` comes from. Gated on `waiting` alone, the count was read
  // once and froze at whatever it was the moment playback started -- zero, on
  // the first viewer to arrive -- and never moved again for the rest of the
  // broadcast.
  //
  // Deliberately NOT `load()`: this refresh may never demote the phase. A
  // transient 404 or a blip would otherwise tear the player down and drop a
  // viewer who is watching perfectly well, which is a far worse failure than a
  // stale number. On an error it keeps what it has.
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

  const master = phase.kind === "ready" ? phase.view.master : "";
  // Built from the token rather than taken from `view.poster`, which is the
  // bare path. A poster is an <img> src: it can carry no header, and the
  // playback cookie is scoped to /playout/ so it is not sent to an /api/v1/
  // URL either. Without the token in the query the poster 404s on every
  // token-protected stream — which is the default.
  const poster = phase.kind === "ready" ? api.playoutPosterUrl(token) : "";

  useEffect(() => {
    const video = videoRef.current;
    if (!video || !master) return;

    // The token rides on the manifest URL. Every request after that one is
    // authorised by the cookie the server set in reply, which is what lets a
    // playlist of relative segment URLs work at all.
    const src = master + (token ? `?t=${encodeURIComponent(token)}` : "");
    let hls: Hls | null = null;
    let retry: number | undefined;

    const start = () => {
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
        // withCredentials is deliberately left alone. The media is same-origin
        // with this page — an embed loads /watch from this server, so its own
        // requests are same-origin too — and same-origin requests carry the
        // playout cookie regardless. Setting it would only matter cross-origin,
        // where it would collide with the wildcard CORS header the media sends
        // and break playback outright.
        hls.loadSource(src);
        hls.attachMedia(video);
        hls.on(Hls.Events.ERROR, (_e, data) => {
          if (!data.fatal) return;
          setPlaying(false);
          hls?.destroy();
          hls = null;
          // A stream that has not started yet has no playlist, which is a
          // normal state rather than an error. Keep looking.
          retry = window.setTimeout(start, 5000);
        });
      } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
        // Safari and most set-top boxes play HLS natively. They cannot set a
        // header, which is the whole reason the token is accepted in the query
        // string and traded for a cookie.
        video.src = src;
      }
    };

    start();
    return () => {
      if (retry) window.clearTimeout(retry);
      hls?.destroy();
    };
  }, [master, token]);

  const embedSnippet =
    `<iframe src="${window.location.origin}${window.location.pathname}" ` +
    `width="854" height="480" frameborder="0" ` +
    `allow="autoplay; fullscreen; picture-in-picture" allowfullscreen></iframe>`;

  const copyEmbed = async () => {
    try {
      await navigator.clipboard.writeText(embedSnippet);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access is denied in a cross-origin iframe and on plain HTTP.
      // The textarea below is always present, so the snippet is still
      // selectable by hand; failing silently is better than an alert.
    }
  };

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
          This stream is private
        </h1>
        <p className="mt-2 max-w-sm text-sm text-neutral-400">
          The link you followed is missing its playback token, or the token has
          been changed. Ask whoever shared it for a current link.
        </p>
      </Shell>
    );
  }

  if (phase.kind === "waiting") {
    return (
      <Shell>
        <h1 className="text-base font-semibold text-neutral-100">{t("player.notLive")}</h1>
        <p className="mt-2 max-w-sm text-sm text-neutral-400">
          This page will start playing on its own when the broadcast begins.
        </p>
      </Shell>
    );
  }

  const { view } = phase;

  return (
    <div className="min-h-dvh bg-neutral-950 text-neutral-100">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4 p-4">
        <div className="relative w-full overflow-hidden rounded-lg bg-black shadow-lg">
          <div className="aspect-video w-full">
            <video
              ref={videoRef}
              className="h-full w-full"
              poster={poster || undefined}
              controls
              playsInline
              autoPlay
              muted
              onPlaying={() => setPlaying(true)}
              onWaiting={() => setPlaying(false)}
            />
          </div>

          {!playing && (
            <div className="pointer-events-none absolute left-3 top-3 rounded bg-black/70 px-2 py-1 text-[11px] text-neutral-300">
              Connecting…
            </div>
          )}

          {playing && (
            <div className="pointer-events-none absolute left-3 top-3 flex items-center gap-1.5 rounded bg-black/70 px-2 py-1 text-[11px] font-medium text-red-400">
              <span className="h-1.5 w-1.5 rounded-full bg-red-500" />
              LIVE
            </div>
          )}
        </div>

        {(view.title || view.description) && (
          <div>
            {view.title && (
              <h1 className="text-lg font-semibold tracking-tight">{view.title}</h1>
            )}
            {view.description && (
              <p className="mt-1 whitespace-pre-wrap text-sm text-neutral-400">
                {view.description}
              </p>
            )}
          </div>
        )}

        <div className="flex flex-wrap items-center gap-3 text-xs text-neutral-500">
          <span>
            {view.viewers} {view.viewers === 1 ? t("player.viewer") : t("player.viewers")}
          </span>
          {view.variants && view.variants.length > 1 && (
            <span>
              {view.variants.length} quality options — your player picks one
              automatically
            </span>
          )}
        </div>

        {/* Hidden inside an embed: nobody wants an embed snippet inside an
            embed, and the nested copy would carry the wrong URL anyway. */}
        {window.self === window.top && (
          <details className="rounded-lg border border-neutral-800 bg-neutral-900/60 p-3">
            <summary className="cursor-pointer text-xs text-neutral-400">
              Embed this player
            </summary>
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
      </div>
    </div>
  );
}

/** Centred frame for the states that have no video to show. */
function Shell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-neutral-950 p-6 text-center text-neutral-100">
      {children}
    </div>
  );
}
