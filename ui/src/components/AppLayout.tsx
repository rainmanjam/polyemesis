import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router-dom";
import {
  Activity,
  AudioLines,
  CalendarClock,
  Check,
  Disc,
  Languages,
  Layers,
  LayoutDashboard,
  Library,
  ListChecks,
  LogOut,
  Menu,
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
import { StatusDot } from "@/components/signature/StatusDot";
import { useLiveData } from "@/hooks/useLiveData";
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
  { to: "/meters", labelKey: "nav.meters", icon: AudioLines },
  { to: "/routing", labelKey: "nav.routing", icon: Sliders },
  { to: "/renditions", labelKey: "nav.renditions", icon: Layers },
  { to: "/playout", labelKey: "nav.playout", icon: Radio },
  { to: "/library", label: "Library", icon: Library },
  { to: "/recordings", labelKey: "nav.recordings", icon: Disc },
  { to: "/clips", label: "Clips", icon: Scissors },
  { to: "/jobs", label: "Jobs", icon: ListChecks },
  { to: "/automation", label: "Automation", icon: CalendarClock },
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
  const location = useLocation();
  const t = useT();
  const stateLabel = useStateLabel();
  const language = useLanguage();

  // Close the drawer on navigation, or a phone user taps a link and stares at
  // the menu they just used.
  useEffect(() => setMobileOpen(false), [location.pathname]);

  const ingest = status?.ingest;
  const ingestTone = toneForState(ingest?.state);
  const liveCount =
    status?.destinations.filter((d) => d.process?.state === "running").length ?? 0;

  return (
    <div className="flex h-dvh flex-col bg-surface">
      {/* ---- top bar: the always-visible answer to "am I on air?" ---- */}
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
            {ingest?.state === "running"
              ? kbps(ingest.progress?.bitrateKbps ?? 0)
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
        <nav
          className={cn(
            "z-40 flex w-44 shrink-0 flex-col gap-0.5 border-r border-border bg-background p-2",
            "max-md:absolute max-md:inset-y-11 max-md:left-0 max-md:transition-transform",
            mobileOpen ? "max-md:translate-x-0" : "max-md:-translate-x-full",
          )}
        >
          {NAV.map(({ to, labelKey, label, icon: Icon, end }) => (
            <NavLink
              key={to}
              to={to}
              end={end}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2 rounded-md px-2 py-1.5 text-[12px] transition-colors",
                  isActive
                    ? "bg-primary-dim text-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )
              }
            >
              <Icon className="h-3.5 w-3.5 shrink-0" />
              {labelKey ? t(labelKey) : label}
            </NavLink>
          ))}
        </nav>

        <main className="min-w-0 flex-1 overflow-y-auto">
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
