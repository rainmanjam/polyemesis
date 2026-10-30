import type { TranslationKey } from "./i18n";

/** The navigation, grouped by the job an operator is doing (A11).
 *
 *  Five labelled sections in place of one flat list of fourteen. The order
 *  inside each section is the order the links appear, and the order of the
 *  sections is the order an operator reads a live console: what is on air,
 *  what plays, who talks to the audience, what keeps it running, then the
 *  settings nobody needs during a broadcast.
 *
 *  This lists ROUTES only. The icon, the tour anchor and the link itself stay
 *  in AppLayout's NAV, where tour-drift.test.ts can find them as literals;
 *  navSections.test.ts checks that every NAV route is placed exactly once. */
export interface NavSection {
  labelKey: TranslationKey;
  routes: string[];
}

export const NAV_SECTIONS: NavSection[] = [
  {
    labelKey: "nav.group.live",
    routes: ["/", "/sources", "/meters", "/routing", "/renditions"],
  },
  {
    labelKey: "nav.group.content",
    routes: ["/playout", "/library", "/recordings", "/clips"],
  },
  {
    labelKey: "nav.group.community",
    routes: ["/chat", "/automation"],
  },
  {
    labelKey: "nav.group.operations",
    routes: ["/jobs", "/monitoring"],
  },
  {
    labelKey: "nav.group.settings",
    routes: ["/settings"],
  },
];
