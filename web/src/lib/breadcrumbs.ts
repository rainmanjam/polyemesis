/* The trail every inner page shows, and the BreadcrumbList that describes it.
 * `Crumbs.astro` renders the first and embeds the second. */
export interface Crumb {
  label: string;
  /** Absolute path. Omitted on a crumb that is only a label (a docs group). */
  href?: string;
}

interface ListItem {
  "@type": "ListItem";
  position: number;
  name: string;
  item?: string;
}

/* Google requires `item` on every entry but the last, so a crumb with no href
 * that is not last is left out and the positions are renumbered. The last
 * crumb keeps its `item` when it has an href. */
export function breadcrumbLd(crumbs: Crumb[], site = "https://polyemesis.com") {
  const kept = crumbs.filter((c, i) => c.href || i === crumbs.length - 1);
  const itemListElement: ListItem[] = kept.map((c, i) => ({
    "@type": "ListItem",
    position: i + 1,
    name: c.label,
    ...(c.href ? { item: site + c.href } : {}),
  }));
  return {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement,
  };
}
