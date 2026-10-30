/* THE ONE TRACK LAYOUT THE HOME PAGE USES (UX Council S7).
 *
 * The home page used to number its tracks three different ways: the problem
 * section said 1 full mix, 2 clean, 3 mic; the patchbay said 1 full mix, 2
 * clean, 3 mic, 4 guest, 5 game, 6 ambience; and the Proof capture said 1 host
 * mic, 2 music bed, 3 co-host, with a legend apologising for the difference. A
 * reader who learned "track 3 is the mic" in one section was told the opposite
 * two scrolls later.
 *
 * The screenshots cannot be the thing that changes: they are photographs of a
 * running install, and that install is seeded from
 * scripts/demo-seed.fixture.json. So the fixture's first programme IS the
 * layout, this file restates it for the site, and src/lib/home-copy.test.ts
 * fails if the two drift apart.
 *
 * `fixtureLabel` is the label the fixture gives the track, quoted exactly; it
 * is what the routing and meters screenshots print. `dbfs` is a nominal level
 * for the patchbay's power-sum readout, not a measurement.
 */
export const TRACK_LAYOUT = [
  { id: 1, name: "Host mic", fixtureLabel: "Host mic", note: "your voice", dbfs: -21.9, slug: "host-mic" },
  { id: 2, name: "Co-host mic", fixtureLabel: "Co-host mic", note: "the second voice", dbfs: -22.4, slug: "co-host-mic" },
  { id: 3, name: "Music bed", fixtureLabel: "Music bed (licensed)", note: "licensed, still claimable", dbfs: -19.5, slug: "music-bed" },
  { id: 4, name: "Room", fixtureLabel: "Crowd / room", note: "crowd and ambience", dbfs: -26.0, slug: "room" },
];

/** The track that carries the copyrighted music, by id. */
export const MUSIC_TRACK = 3;
