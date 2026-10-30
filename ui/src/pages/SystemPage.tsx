import { PageHeader } from "@/components/AppLayout";
import { DebugSettings } from "@/components/DebugSettings";
import { McpStatusLine } from "@/components/McpAccessCard";
import { useT } from "@/lib/i18n";

/* THE SYSTEM PAGE (A17). Debug recording used to be the fifth Settings tab,
 * beside Security, for a control reached a few times a year when something
 * has already gone wrong. Settings is where an operator configures; this is
 * where they look at the machine. /settings?tab=debug still lands here, so a
 * bookmarked link or a support thread's instructions keep working. */
export function SystemPage() {
  const t = useT();
  return (
    <div className="p-3">
      <PageHeader title={t("nav.system")} subtitle={t("system.subtitle")} />
      <div className="flex max-w-3xl flex-col gap-3">
        {/* Whether an AI assistant can reach this server, at a glance: the
            same line the Automation card shows, linking to it. */}
        <McpStatusLine />
        <DebugSettings />
      </div>
    </div>
  );
}
