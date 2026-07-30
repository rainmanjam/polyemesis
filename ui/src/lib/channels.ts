/** Standard channel names by count, so a 5.1 track labels its channels
 *  correctly instead of showing 1..6.
 *
 *  Lived in AudioMeter.tsx, which is not where it belongs: three of its four
 *  callers are not the meter (TrackRows, MixMatrix and MetersPage), and the
 *  function itself has nothing to do with drawing. Keeping it next to the meter
 *  also broke Fast Refresh for the meter -- a file exporting both a component
 *  and a plain function cannot be hot-swapped, so touching this switch used to
 *  full-reload a page whose whole purpose is a live signal.
 *
 *  The layout order follows FFmpeg's channel layouts, because that is where the
 *  channel counts come from. A track arrives as `5.1` and gets split by index,
 *  so index 3 has to mean LFE here for the same reason it does there. */
export function channelLabels(count: number): string[] {
  switch (count) {
    case 1:
      return ["M"];
    case 2:
      return ["L", "R"];
    case 3:
      return ["L", "R", "C"];
    case 4:
      return ["L", "R", "Ls", "Rs"];
    case 5:
      return ["L", "R", "C", "Ls", "Rs"];
    case 6:
      return ["L", "R", "C", "LFE", "Ls", "Rs"];
    case 7:
      return ["L", "R", "C", "LFE", "Cs", "Ls", "Rs"];
    case 8:
      return ["L", "R", "C", "LFE", "Lb", "Rb", "Ls", "Rs"];
    default:
      // Not a layout FFmpeg names, so numbering is the honest answer. Inventing
      // labels for a 12-channel track would put words on channels nobody has
      // told us the meaning of.
      return Array.from({ length: count }, (_, i) => String(i + 1));
  }
}
