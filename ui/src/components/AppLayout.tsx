import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router";

import { UpdateBanner } from "./UpdateBanner";
import {
  Activity,
  AudioLines,
  CalendarClock,
  Check,
  ChevronLeft,
  ChevronRight,
  Disc,
  Languages,
  Layers,
  LayoutDashboard,
  RadioTower,
  Library,
  ListChecks,
  LogOut,
  Menu,
  MessagesSquare,
  Radio,
  Scissors,
  Settings as SettingsIcon,
  Sliders,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { StatusDot } from "@/components/signature/StatusDot";
import { useIngestLive, useLiveData } from "@/hooks/useLiveData";
import { useNavCollapsed } from "@/hooks/useNavCollapsed";
import { toneForState } from "@/lib/signal";
import { duration, kbps } from "@/lib/format";
import {
  LANGUAGES,
  setLanguage,
  useLanguage,
  useStateLabel,
  useT,
  type TranslationKey,
} from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A nav entry names itself either through the catalogue or, for a page whose
 *  key has not been added to en.json yet, with a literal. The literal arm is a
 *  bridge, not a pattern: a page that ships before its translation key should
 *  still appear in the nav rather than render a raw key at the operator. */
type NavItem = {
  to: string;
  icon: React.ComponentType<{ className?: string }>;
  end?: boolean;
} & ({ labelKey: TranslationKey; label?: never } | { label: string; labelKey?: never });

const NAV: NavItem[] = [
  { to: "/", labelKey: "nav.dashboard", icon: LayoutDashboard, end: true },
  { to: "/sources", labelKey: "nav.sources", icon: RadioTower },
  { to: "/meters", labelKey: "nav.meters", icon: AudioLines },
  { to: "/routing", labelKey: "nav.routing", icon: Sliders },
  { to: "/renditions", labelKey: "nav.renditions", icon: Layers },
  { to: "/playout", labelKey: "nav.playout", icon: Radio },
  { to: "/library", labelKey: "nav.library", icon: Library },
  { to: "/recordings", labelKey: "nav.recordings", icon: Disc },
  { to: "/clips", labelKey: "nav.clips", icon: Scissors },
  { to: "/chat", labelKey: "nav.chat", icon: MessagesSquare },
  { to: "/jobs", labelKey: "nav.jobs", icon: ListChecks },
  { to: "/automation", labelKey: "nav.automation", icon: CalendarClock },
  { to: "/monitoring", labelKey: "nav.monitoring", icon: Activity },
  { to: "/settings", labelKey: "nav.settings", icon: SettingsIcon },
];

export function AppLayout({
  username,
  onSignOut,
}: {
  username: string;
  onSignOut: () => void;
}) {
  const { status, connected } = useLiveData();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [navCollapsed, toggleNav] = useNavCollapsed();
  const location = useLocation();
  const t = useT();
  const stateLabel = useStateLabel();
  const language = useLanguage();

  // Close the drawer on navigation, or a phone user taps a link and stares at
  // the menu they just used.
  useEffect(() => setMobileOpen(false), [location.pathname]);

  const ingest = status?.ingest;
  // BYTES ARRIVING, not a process running.
  //
  // useIngestLive is the app's one definition of "a broadcast is going out",
  // and its own comment says `status.ingest.state === "running"` is not it.
  // This header was the last place still asking the wrong question, and for SRT
  // the answer was not merely imprecise but inverted: engine.reconcileIngest
  // returns early for SRT on purpose — srtserver delivers datagrams straight
  // into the hub, and a second thing on that socket would crash-loop — so
  // `ingest` is null, `stateLabel(undefined)` is "Offline", and every healthy
  // SRT install said its ingest was down in the most prominent status in the
  // chrome. A committed screenshot showed it beside three live destinations.
  //
  // The process is still consulted for the bitrate below, where it exists.
  const ingestLive = useIngestLive();
  const ingestTone = ingestLive ? "live" : toneForState(ingest?.state);
  const liveCount =
    status?.destinations.filter((d) => d.process?.state === "running").length ?? 0;

  return (
    <div className="flex h-dvh flex-col bg-surface">
      {/* ---- top bar: the always-visible answer to "am I on air?" ---- */}
      <UpdateBanner />

      <header className="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-background px-3">
        <Button
          variant="ghost"
          size="icon-sm"
          className="md:hidden"
          onClick={() => setMobileOpen((v) => !v)}
          aria-label={t("chrome.toggleNav")}
        >
          {mobileOpen ? <X /> : <Menu />}
        </Button>

        <div className="flex items-center gap-2">
          <div className="flex h-5 w-5 items-center justify-center rounded bg-primary/15">
            <AudioLines className="h-3 w-3 text-primary" />
          </div>
          <span className="text-[13px] font-semibold tracking-tight">polyemesis</span>
        </div>

        <div className="ml-2 hidden items-center gap-2 sm:flex">
          <StatusDot tone={ingestTone} />
          <span className="text-[11px] text-muted-foreground">{t("chrome.ingest")}</span>
          <span className="tnum font-mono text-[11px]">
            {/* The bitrate when there is a process to read it from, and the
                state otherwise. An SRT source is live without a child, so it
                has no bitrate to show here — saying "Running" is the honest
                answer rather than printing 0 kbps at a stream that is fine. */}
            {ingest?.state === "running"
              ? kbps(ingest.progress?.bitrateKbps ?? 0)
              : ingestLive
                ? stateLabel("running")
                : stateLabel(ingest?.state)}
          </span>
        </div>

        <div className="ml-auto flex items-center gap-3">
          {liveCount > 0 && (
            <Badge variant="live" className="hidden sm:inline-flex">
              <StatusDot tone="live" size="sm" />
              {t("chrome.liveCount", { count: liveCount })}
            </Badge>
          )}
          {ingest?.state === "running" && (
            <span className="tnum hidden font-mono text-[11px] text-muted-foreground sm:inline">
              {duration(ingest.uptimeSec)}
            </span>
          )}
          {/* Socket health matters: a disconnected UI showing stale numbers is
              worse than one that admits it is stale. */}
          {!connected && (
            <Badge variant="warn" title={t("chrome.socketOfflineHint")}>
              {t("chrome.socketOffline")}
            </Badge>
          )}
          <span className="hidden text-[11px] text-muted-foreground lg:inline">{username}</span>

          {/* Sits in the chrome rather than on Settings: the operator who needs
              it cannot necessarily read the nav item that would lead there. */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={t("chrome.language")}
                title={t("chrome.language")}
              >
                <Languages />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuLabel>{t("chrome.language")}</DropdownMenuLabel>
              {LANGUAGES.map((lang) => (
                <DropdownMenuItem key={lang.code} onSelect={() => setLanguage(lang.code)}>
                  <Check className={cn(lang.code === language ? "opacity-100" : "opacity-0")} />
                  {lang.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <Button
            variant="ghost"
            size="icon-sm"
            onClick={onSignOut}
            aria-label={t("chrome.signOut")}
          >
            <LogOut />
          </Button>
        </div>
      </header>

      <div className="flex min-h-0 flex-1">
        {/* ---- sidebar ---- */}
        <TooltipProvider delayDuration={0}>
          <nav
            // No aria-expanded here: it is not a supported state for
            // role="navigation" (the implicit role of <nav>) per the ARIA
            // spec's supported-states table. The toggle button carries the
            // state instead -- button role does support it, and it is the
            // control the state actually describes.
            className={cn(
              // min-h-0 + overflow-y-auto: the nav owns its overflow, the way
              // <main> below already does.
              //
              // Without them it was the one part of an h-dvh shell that could
              // not be clipped. The links plus the mt-auto toggle are taller
              // than the row on a short window, `overflow` defaulted to
              // `visible`, and the toggle hung 14px below the fold -- which
              // extended the DOCUMENT, so the browser drew a page scrollbar
              // right next to the one <main> was already showing. Two bars, and
              // the outer one scrolled 14px of nothing.
              //
              // Measured at 936x500: body scrollHeight 514 vs clientHeight 500,
              // exactly the toggle's height. Above ~620px tall the nav fits and
              // nothing here has any effect.
              "z-40 flex min-h-0 shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-border bg-background p-2 transition-[width]",
              navCollapsed ? "md:w-12" : "md:w-44",
              // The drawer is always full width: a collapsed rail behind a
              // hamburger would be an icon strip nobody asked for, and the
              // drawer already solves the problem collapsing solves.
              "w-44",
              "max-md:absolute max-md:inset-y-11 max-md:left-0 max-md:transition-transform",
              mobileOpen ? "max-md:translate-x-0" : "max-md:-translate-x-full",
            )}
          >
            {NAV.map(({ to, labelKey, label, icon: Icon, end }) => {
              const text = labelKey ? t(labelKey) : label;
              // Tooltip/TooltipTrigger mount UNCONDITIONALLY -- only
              // TooltipContent below is gated on navCollapsed. Earlier this
              // map returned a bare <NavLink> when expanded and a
              // <Tooltip><TooltipTrigger asChild>...</Tooltip> when collapsed:
              // same `key`, but a DIFFERENT element type at the same array
              // position, so toggling forced React to unmount and remount the
              // whole subtree instead of reconciling it. The <a> DOM node was
              // destroyed and recreated, and with it any focus that was on
              // it -- a keyboard user tabbed to a link, pressed Ctrl/Cmd+B,
              // and landed on <body>. Keeping the trigger's element type
              // constant across the toggle is what lets React diff instead of
              // replace, so focus survives.
              return (
                <Tooltip key={to}>
                  <TooltipTrigger asChild>
                    <NavLink
                      key={to}
                      to={to}
                      end={end}
                      // The icon is aria-hidden (lucide's default when an icon
                      // gets no a11y prop of its own) and the label span below is
                      // display:none while collapsed, so without this the
                      // collapsed rail is fourteen unnamed links -- the
                      // accessibility tree has a URL and nothing else to read.
                      // Same t(labelKey) call as the visible label and the
                      // tooltip, so the three can never disagree.
                      aria-label={text}
                      // className must stay a plain STRING, never the
                      // `({ isActive }) => string` function form NavLink also
                      // accepts: Radix Slot (what TooltipTrigger asChild uses)
                      // concatenates className as a string, so a function
                      // className is stringified into the class attribute --
                      // literally the function's source text -- before Router
                      // ever calls it, and every utility class on it is lost.
                      // Drive the active look from `aria-current="page"`
                      // instead, which NavLink already sets on itself.
                      className={cn(
                        "flex items-center gap-2 rounded-md px-2 py-1.5 text-[12px] transition-colors",
                        navCollapsed && "md:justify-center md:px-0",
                        "text-muted-foreground hover:bg-accent hover:text-foreground",
                        "aria-[current=page]:bg-primary-dim aria-[current=page]:text-foreground",
                      )}
                    >
                      <Icon className="h-3.5 w-3.5 shrink-0" />
                      {/* md:hidden, which compiles to display:none and therefore
                          leaves the accessibility tree. NOT sr-only: the name
                          still needs to reach the accessibility tree while
                          collapsed, which is what aria-label on the NavLink
                          above is for -- the visible label and the accessible
                          name are two different mechanisms on purpose, so the
                          rail can stay visually icon-only while still reading
                          identically to the expanded nav non-visually.

                          Conditioned on the breakpoint AND the state, because the
                          drawer below md keeps its labels even while collapsed. */}
                      <span className={cn(navCollapsed && "md:hidden")}>{text}</span>
                    </NavLink>
                  </TooltipTrigger>
                  {/* Only while collapsed. Expanded, the label is already on
                      screen and a tooltip repeating it is noise that also
                      delays every hover. Rendering no TooltipContent at all
                      (rather than an empty one) means no tooltip appears. */}
                  {navCollapsed && (
                    // aria-hidden: the link already carries the name via its
                    // own aria-label above, so a screen reader announcing the
                    // tooltip's aria-describedby too repeats it verbatim --
                    // "Dashboard, link, Dashboard". This content is a visual
                    // affordance for sighted users only.
                    <TooltipContent side="right" aria-hidden>
                      {text}
                    </TooltipContent>
                  )}
                </Tooltip>
              );
            })}

            {/* Pinned to the bottom, so the target does not move as the width
                changes. mt-auto rather than a spacer div. */}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={toggleNav}
              aria-label={t("chrome.toggleNav")}
              aria-expanded={!navCollapsed}
              className="mt-auto hidden self-end md:flex"
            >
              {navCollapsed ? <ChevronRight /> : <ChevronLeft />}
            </Button>
          </nav>
        </TooltipProvider>

        {/* `relative` is load-bearing, not decoration.

            The shell is h-dvh, so nothing should ever scroll the document —
            only this pane and the sidebar. An absolutely positioned descendant
            with no positioned ancestor resolves against the initial containing
            block instead, which is the html element, so it extends
            documentElement.scrollHeight without touching body's. That produces
            a second scrollbar down the right-hand side next to this pane's own,
            and measuring body tells you nothing because body is still exactly
            the viewport height.

            The Dashboard's aria-live <output> did precisely this: Tailwind's
            .sr-only is `position: absolute` with no top/left, so it sat at its
            static position ~951px down and stretched the document to 952px
            against a 900px viewport. Making this pane a containing block keeps
            that — and anything else a page renders — inside the scroller where
            it belongs. */}
        <main className="relative min-w-0 flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

/** Consistent page header. Every page uses it so titles, subtitles and
 *  actions sit in the same place regardless of what the page does. */
export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: React.ReactNode;
}) {
  return (
    <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
      <div>
        <h1 className="text-[15px] font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-0.5 text-[11px] text-muted-foreground">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-1.5">{actions}</div>}
    </div>
  );
}
