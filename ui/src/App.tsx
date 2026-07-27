import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { Toaster } from "sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataProvider } from "@/hooks/useLiveData";
import { api, ApiError } from "@/lib/api";
import { AppLayout } from "@/components/AppLayout";
import { AuthScreen } from "@/pages/AuthScreen";
import { AutomationPage } from "@/pages/AutomationPage";
import { ClipsPage } from "@/pages/ClipsPage";
import { LibraryPage } from "@/pages/LibraryPage";
import { Dashboard } from "@/pages/Dashboard";
import { MetersPage } from "@/pages/MetersPage";
import { RoutingPage } from "@/pages/RoutingPage";
import { RenditionsPage } from "@/pages/RenditionsPage";
import { RecordingsPage } from "@/pages/RecordingsPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { Loader2 } from "lucide-react";

// Monitoring is the only page that pulls in recharts — several hundred
// kilobytes nobody watching the dashboard has asked for. Split it out so the
// first paint pays for the pages it actually shows.
const MonitoringPage = lazy(() =>
  import("@/pages/MonitoringPage").then((m) => ({ default: m.MonitoringPage })),
);

const PlayoutPage = lazy(() =>
  import("@/pages/PlayoutPage").then((m) => ({ default: m.PlayoutPage })),
);

// The clip editor is reached from a recording in the library, never from the
// nav, and it carries a canvas timeline and a whole transcript nobody browsing
// the dashboard has asked to download. Split for the same reason Monitoring is.
//
// /clips/:id is the editor for one recording; /clips with no id is the live
// ring buffer, which is a different feature entirely. They share a path prefix
// and nothing else — see internal/api/clips.go.
const ClipEditor = lazy(() =>
  import("@/pages/ClipEditor").then((m) => ({ default: m.ClipEditor })),
);

// The public player is split for a different reason than Monitoring is: not to
// save the admin a download, but to save a VIEWER one. Somebody following a
// shared link is not signing in and has no use for the console, so /watch is
// resolved before the auth gate and pulls its own chunk.
// The jobs page carries the whole resource-policy editor and is opened when
// something needs explaining, not on every visit. Split for the same reason
// Monitoring is.
const JobsPage = lazy(() =>
  import("@/pages/JobsPage").then((m) => ({ default: m.JobsPage })),
);

// Chat opens its own socket and buffers messages for as long as it is mounted,
// so it is split for a reason the other lazy routes are not: a session that
// never opens the pane should not pay for it at all.
const ChatPage = lazy(() =>
  import("@/pages/ChatPage").then((m) => ({ default: m.ChatPage })),
);

const PublicPlayer = lazy(() =>
  import("@/pages/PublicPlayer").then((m) => ({ default: m.PublicPlayer })),
);

/** Whether this page load is the public player.
 *
 *  Read from the raw location rather than from a route, because the decision has
 *  to be made before <BrowserRouter> exists — the auth gate below runs first,
 *  and a viewer has no session for it to resolve. */
function isWatchRoute(): boolean {
  const p = window.location.pathname;
  return p === "/watch" || p.startsWith("/watch/");
}

/** Placeholder for a chunk still in flight. Same weight as the app's own
 *  loading states, so a split route reads as "loading", not as a flash. */
function RouteFallback() {
  return (
    <div className="flex h-64 items-center justify-center">
      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
    </div>
  );
}

type Gate =
  | { phase: "loading" }
  | { phase: "setup" }
  | { phase: "login" }
  | { phase: "ready"; username: string };

export default function App() {
  const [gate, setGate] = useState<Gate>({ phase: "loading" });
  // Fixed for the life of the page: a viewer never navigates out of /watch, and
  // an admin never navigates into it without a full reload.
  const [watching] = useState(isWatchRoute);

  const resolveGate = useCallback(async () => {
    if (watching) return;
    try {
      const { needsSetup } = await api.setupStatus();
      if (needsSetup) {
        setGate({ phase: "setup" });
        return;
      }
      const me = await api.me();
      setGate({ phase: "ready", username: me.username });
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setGate({ phase: "login" });
        return;
      }
      // Anything else (server restarting, network blip) is treated as
      // "show the login screen" rather than a dead white page.
      setGate({ phase: "login" });
    }
  }, [watching]);

  useEffect(() => {
    void resolveGate();
  }, [resolveGate]);

  const signOut = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setGate({ phase: "login" });
    }
  }, []);

  // Ahead of every gate: no session, no setup check, no toaster, no live-data
  // socket. A viewer following a shared link gets the player and nothing else.
  if (watching) {
    return (
      <Suspense fallback={<RouteFallback />}>
        <PublicPlayer />
      </Suspense>
    );
  }

  if (gate.phase === "loading") {
    return (
      <div className="flex h-dvh items-center justify-center bg-surface">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (gate.phase === "setup" || gate.phase === "login") {
    return (
      <>
        <AuthScreen mode={gate.phase} onDone={resolveGate} />
        <Toaster theme="dark" position="bottom-right" richColors closeButton />
      </>
    );
  }

  return (
    <BrowserRouter>
      <TooltipProvider delayDuration={300}>
        {/* The live data socket is opened once, at the top, and shared by every
            page. Opening one per page would mean N sockets and N status feeds. */}
        <LiveDataProvider>
          <Routes>
            <Route element={<AppLayout username={gate.username} onSignOut={signOut} />}>
              <Route path="/" element={<Dashboard />} />
              <Route path="/meters" element={<MetersPage />} />
              <Route path="/routing" element={<RoutingPage />} />
              <Route path="/routing/:id" element={<RoutingPage />} />
              <Route path="/renditions" element={<RenditionsPage />} />
              <Route
                path="/playout"
                element={
                  <Suspense fallback={<RouteFallback />}>
                    <PlayoutPage />
                  </Suspense>
                }
              />
              <Route path="/library" element={<LibraryPage />} />
              <Route path="/recordings" element={<RecordingsPage />} />
              <Route path="/clips" element={<ClipsPage />} />
              {/* The clip editor is per-recording and reached from the
                  library, never from the nav. */}
              <Route
                path="/clips/:id"
                element={
                  <Suspense fallback={<RouteFallback />}>
                    <ClipEditor />
                  </Suspense>
                }
              />
              <Route
                path="/jobs"
                element={
                  <Suspense fallback={<RouteFallback />}>
                    <JobsPage />
                  </Suspense>
                }
              />
              <Route
                path="/chat"
                element={
                  <Suspense fallback={<RouteFallback />}>
                    <ChatPage />
                  </Suspense>
                }
              />
              <Route path="/automation" element={<AutomationPage />} />
              <Route
                path="/monitoring"
                element={
                  <Suspense fallback={<RouteFallback />}>
                    <MonitoringPage />
                  </Suspense>
                }
              />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </LiveDataProvider>
        <Toaster theme="dark" position="bottom-right" richColors closeButton />
      </TooltipProvider>
    </BrowserRouter>
  );
}
