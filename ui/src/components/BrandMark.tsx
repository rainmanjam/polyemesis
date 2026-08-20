/* THE PRODUCT'S MARK, and there is exactly one of it.
 *
 * Ported verbatim from the website's nav (web/src/components/Nav.astro): five
 * ingest tracks at unequal levels, summing left to right. Five rather than six
 * because a sixth at this spacing falls outside the 22-wide viewBox, and the
 * mark should read as "several tracks" rather than invite counting.
 *
 * Before this the product wore three different marks. The site had these bars,
 * the app header and the sign-in screen had lucide's <AudioLines> -- a generic
 * waveform belonging to no product in particular -- and the app's favicon had a
 * THIRD variant: four bars, one colour, on a different background. Someone
 * arriving from the website met a different logo on the page where they type
 * their password, which is the one screen where looking like the right site
 * matters most.
 *
 * THE COLOURS ARE LITERAL AND THAT IS DELIBERATE. They happen to equal three of
 * the app's tokens today -- --primary, --meter-low and --armed -- and wiring
 * them to those tokens is the tempting move. It is wrong: those tokens carry
 * MEANING elsewhere in this UI (a destination's state, a meter's band), so
 * repointing one to serve a design need would silently restyle the logo, and a
 * mark that drifts with the palette stops matching the site it is supposed to
 * match. A logo is a constant, not a themed surface.
 */
export function BrandMark({ size = 20, className }: { size?: number; className?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 22 22"
      fill="none"
      className={className}
      aria-hidden="true"
    >
      <rect x="1" y="8" width="2.5" height="6" rx="1.25" fill="#5b7fc7" />
      <rect x="5" y="4" width="2.5" height="14" rx="1.25" fill="#5b7fc7" />
      <rect x="9" y="6.5" width="2.5" height="9" rx="1.25" fill="#3ecf6d" />
      <rect x="13" y="2" width="2.5" height="18" rx="1.25" fill="#3ecf6d" />
      <rect x="17" y="7" width="2.5" height="8" rx="1.25" fill="#45b5d0" />
    </svg>
  );
}
