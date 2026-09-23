import { Component, Suspense, type ErrorInfo, type ReactNode } from "react";
import { RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { isChunkLoadError } from "@/lib/chunkReload";
import { useT } from "@/lib/i18n";

/* WHAT THE CONSOLE SHOWS WHEN A PAGE THROWS.
 *
 * There used to be no error boundary at all, so any render error -- most
 * often a lazy route's chunk that 404s because the server was upgraded under
 * an open tab -- unmounted the whole tree and left an empty <div id="root">.
 * Back did not help: the failed import is cached, so the page stayed blank
 * until the operator thought to reload.
 *
 * Two layers. LazyBoundary wraps each lazy chunk -- every lazy route, and the dashboard's preview player -- in its OWN boundary and
 * Suspense, so a page that fails takes itself down and leaves the nav, the
 * live socket and every other page working. AppBoundary wraps everything, as
 * the last word for an error outside any route. Both end in the same notice:
 * what happened, and a Reload button, which is the one action that fixes a
 * missing chunk. lib/chunkReload.ts tries that reload automatically, once,
 * before an error gets this far. */

interface BoundaryProps {
  children: ReactNode;
  fallback: (error: unknown) => ReactNode;
}

interface BoundaryState {
  error: unknown;
  failed: boolean;
}

class ErrorBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { error: null, failed: false };

  static getDerivedStateFromError(error: unknown): BoundaryState {
    return { error, failed: true };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    // Kept in the console for a bug report; the operator sees the notice.
    console.error("polyemesis: a page failed to render", error, info.componentStack);
  }

  render() {
    return this.state.failed ? this.props.fallback(this.state.error) : this.props.children;
  }
}

/** The notice both boundaries render. */
function CrashNotice({ error, fullScreen }: { error: unknown; fullScreen?: boolean }) {
  const t = useT();
  const stale = isChunkLoadError(error);
  return (
    <div
      role="alert"
      className={
        fullScreen
          ? "flex h-dvh flex-col items-center justify-center gap-3 bg-surface p-6 text-center"
          : "flex h-64 flex-col items-center justify-center gap-3 p-6 text-center"
      }
    >
      <p className="text-sm font-medium">{stale ? t("app.staleTitle") : t("app.crashTitle")}</p>
      <p className="max-w-md text-tiny text-muted-foreground">
        {stale ? t("app.staleBody") : t("app.crashBody")}
      </p>
      <Button size="sm" onClick={() => window.location.reload()}>
        <RotateCcw /> {t("app.reload")}
      </Button>
    </div>
  );
}

/** A lazy chunk's boundary and loading state, together.
 *
 *  Together on purpose: a <Suspense> for a lazy chunk with no boundary around
 *  it is the blank page this file exists to prevent, and lib/chunkReload.test.ts
 *  refuses a bare <Suspense> anywhere in src/ outside this file. */
export function LazyBoundary({ children, fallback }: { children: ReactNode; fallback: ReactNode }) {
  return (
    <ErrorBoundary fallback={(error) => <CrashNotice error={error} />}>
      <Suspense fallback={fallback}>{children}</Suspense>
    </ErrorBoundary>
  );
}

/** The outermost boundary, for an error no route caught. */
export function AppBoundary({ children }: { children: ReactNode }) {
  return (
    <ErrorBoundary fallback={(error) => <CrashNotice error={error} fullScreen />}>
      {children}
    </ErrorBoundary>
  );
}
