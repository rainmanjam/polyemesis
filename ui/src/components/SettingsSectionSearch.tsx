import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { searchSettings, type SettingsTab } from "@/lib/settingsSearch";
import { useT } from "@/lib/i18n";

/** A search box over the settings sections. Picking a result hands the section
 *  back to the page, which sets the same `?tab=` the rail sets, so a search
 *  result is a link like any other. */
export function SettingsSectionSearch({
  labels,
  onPick,
}: {
  labels: Record<SettingsTab, string>;
  onPick: (tab: SettingsTab) => void;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const hits = searchSettings(query, labels);
  const typed = query.trim() !== "";

  return (
    <div className="mb-3 flex flex-col gap-2">
      <Input
        type="search"
        className="max-w-sm"
        aria-label={t("settings.search")}
        placeholder={t("settings.search")}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />
      {typed && (
        <div className="flex flex-wrap gap-1.5">
          {hits.length === 0 ? (
            <span className="text-tiny text-muted-foreground">{t("settings.noMatches")}</span>
          ) : (
            hits.map((tab) => (
              <Button
                key={tab}
                variant="outline"
                size="sm"
                onClick={() => {
                  setQuery("");
                  onPick(tab);
                }}
              >
                {labels[tab]}
              </Button>
            ))
          )}
        </div>
      )}
    </div>
  );
}
