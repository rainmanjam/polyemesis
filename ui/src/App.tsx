import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { Toaster } from "sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { LiveDataProvider } from "@/hooks/useLiveData";
import { api, ApiError } from "@/lib/api";
import { AppLayout } from "@/components/AppLayout";
import { AuthScreen } from "@/pages/AuthScreen";
import { Dashboard } from "@/pages/Dashboard";
import { MetersPage } from "@/pages/MetersPage";
import { RoutingPage } from "@/pages/RoutingPage";
import { RecordingsPage } from "@/pages/RecordingsPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { Loader2 } from "lucide-react";

// Monitoring is the only page that pulls in recharts — several hundred
// kilobytes nobody watching the dashboard has asked for. Split it out so the
// first paint pays for the pages it actually shows.
const MonitoringPage = lazy(() =>
  import("@/pages/MonitoringPage").then((m) => ({ default: m.MonitoringPage })),
);

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

  const resolveGate = useCallback(async () => {
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
  }, []);

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
              <Route path="/recordings" element={<RecordingsPage />} />
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
