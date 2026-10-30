/** The sub-rail's sections, in rail order. The values are the `?tab=` values
 *  the settings page already reads, so a link to a section keeps working. */
export const SETTINGS_TABS = ["ingest", "pipeline", "platforms", "security", "debug"] as const;

export type SettingsTab = (typeof SETTINGS_TABS)[number];

/** Finds sections whose translated name contains the query. Matching the
 *  label the operator reads, rather than an English keyword list, means a
 *  search works in every locale. This searches section names only; it does not
 *  yet reach individual fields. */
export function searchSettings(
  query: string,
  labels: Record<SettingsTab, string>,
): SettingsTab[] {
  const needle = query.trim().toLowerCase();
  if (needle === "") return [];
  return SETTINGS_TABS.filter((tab) => labels[tab].toLowerCase().includes(needle));
}
