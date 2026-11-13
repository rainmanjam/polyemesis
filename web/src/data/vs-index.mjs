/* The five "compared one at a time" entries on /comparison, in the order the
 * page lists them. The wording is the page's own, moved here word for word so a
 * test can read it. `wide` marks the one that is a different shape from the
 * rest (Streamlabs is the only cloud relay in the set: the other four run on
 * your own machine, as a plugin or a server).
 *
 * @type {{ slug: string; href: string; name: string; kind: string; line: string; wide?: boolean }[]} */
export const VS_INDEX = [
  {
    slug: "aitum-multistream",
    href: "/vs/aitum-multistream",
    name: "Aitum Multistream",
    kind: "OBS plugin · free · GPL-2.0",
    line: "It already sends different audio per platform. The argument is about where the mix gets built, and what a third one costs.",
  },
  {
    slug: "obs-multi-rtmp",
    href: "/vs/obs-multi-rtmp",
    name: "obs-multi-rtmp",
    kind: "OBS plugin · free · GPL-2.0",
    line: "Get from OBS means fanning out costs one encode, not four. What it cannot share is your uplink: the same bytes, sent once per platform.",
  },
  {
    slug: "restreamer",
    href: "/vs/restreamer",
    name: "Restreamer and restream.io",
    kind: "Server · SaaS · two different products",
    line: "Two unrelated things share the name. Neither can build a second mix at all, and one of them bills per channel.",
  },
  {
    slug: "streamelements",
    href: "/vs/streamelements",
    name: "StreamElements",
    kind: "OBS plugin · free · Windows only",
    line: "The dashboard everyone knows sends no video at all. The multistreaming is SE.Live, which is Windows-only and speaks no SRT.",
  },
  {
    slug: "streamlabs",
    href: "/vs/streamlabs",
    name: "Streamlabs",
    kind: "Cloud relay · Ultra subscription",
    line: "Their servers do the fan-out, and multistreaming proper is behind the paid tier. The free path stops at two destinations.",
    wide: true,
  },
];
