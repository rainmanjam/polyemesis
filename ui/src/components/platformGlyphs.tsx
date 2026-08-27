import type { SVGProps } from "react";

/* ===========================================================================
   THE PLATFORMS' OWN MARKS.

   Chat rows used to be labelled with lucide approximations -- a gamepad for
   Twitch, a thumbs-up for Facebook, a monitor for YouTube, a lightning bolt
   for Kick. Every one of them is a guess at a brand, and three of the four
   read as something else entirely: the thumbs-up is a reaction, the gamepad is
   a games category, the bolt is a status. On a page whose entire claim is
   "every platform in one timeline", the icon that says WHICH platform is the
   load-bearing element, and it was the one thing being approximated.

   Drawn in currentColor, which is what keeps the design rule in lib/chat.ts
   intact: the accent a platform gets still comes from the theme's signal
   palette, not from the brand's own hex. The SHAPE identifies the platform;
   the COLOUR stays the kit's. That was always the distinction the rule was
   protecting -- it is a rule about adding saturated colours, and it never
   required the marks themselves to be invented.

   Paths are Simple Icons (https://simplecons.org), CC0 1.0, copied verbatim
   from the published SVGs rather than redrawn. The logos remain the trade
   marks of their respective owners and are used here only to identify the
   service each message came from.
   =========================================================================== */

/** All four share a viewBox and a fill, and differ only in the path -- so the
 *  wrapper is written once. A component per platform rather than one taking a
 *  name, because lib/chat.ts stores the COMPONENT and a lookup that can miss
 *  would put a blank square where the platform belongs. */
function glyph(title: string, d: string) {
  const Glyph = (props: SVGProps<SVGSVGElement>) => (
    <svg
      viewBox="0 0 24 24"
      fill="currentColor"
      role="img"
      // Decorative BY DEFAULT and overridable: every caller in ChatPanel sits
      // beside the platform's name in text, so announcing the mark as well
      // reads the platform twice to a screen reader. The one caller that has
      // no adjacent label passes its own aria-label, which wins.
      aria-hidden="true"
      focusable="false"
      {...props}
    >
      <title>{title}</title>
      <path d={d} />
    </svg>
  );
  Glyph.displayName = `${title}Glyph`;
  return Glyph;
}

export const YouTubeGlyph = glyph(
  "YouTube",
  "M23.498 6.186a3.016 3.016 0 0 0-2.122-2.136C19.505 3.545 12 3.545 12 3.545s-7.505 0-9.377.505A3.017 3.017 0 0 0 .502 6.186C0 8.07 0 12 0 12s0 3.93.502 5.814a3.016 3.016 0 0 0 2.122 2.136c1.871.505 9.376.505 9.376.505s7.505 0 9.377-.505a3.015 3.015 0 0 0 2.122-2.136C24 15.93 24 12 24 12s0-3.93-.502-5.814zM9.545 15.568V8.432L15.818 12l-6.273 3.568z",
);

export const TwitchGlyph = glyph(
  "Twitch",
  "M11.571 4.714h1.715v5.143H11.57zm4.715 0H18v5.143h-1.714zM6 0L1.714 4.286v15.428h5.143V24l4.286-4.286h3.428L22.286 12V0zm14.571 11.143l-3.428 3.428h-3.429l-3 3v-3H6.857V1.714h13.714Z",
);

export const FacebookGlyph = glyph(
  "Facebook",
  "M9.101 23.691v-7.98H6.627v-3.667h2.474v-1.58c0-4.085 1.848-5.978 5.858-5.978.401 0 .955.042 1.468.103a8.68 8.68 0 0 1 1.141.195v3.325a8.623 8.623 0 0 0-.653-.036 26.805 26.805 0 0 0-.733-.009c-.707 0-1.259.096-1.675.309a1.686 1.686 0 0 0-.679.622c-.258.42-.374.995-.374 1.752v1.297h3.919l-.386 2.103-.287 1.564h-3.246v8.245C19.396 23.238 24 18.179 24 12.044c0-6.627-5.373-12-12-12s-12 5.373-12 12c0 5.628 3.874 10.35 9.101 11.647Z",
);

export const KickGlyph = glyph(
  "Kick",
  "M1.333 0h8v5.333H12V2.667h2.667V0h8v8H20v2.667h-2.667v2.666H20V16h2.667v8h-8v-2.667H12v-2.666H9.333V24h-8Z",
);
