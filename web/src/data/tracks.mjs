/* THE ONE TRACK LAYOUT THE FEATURES PAGE USES (UX Council S7).
 *
 * The screenshots cannot be the thing that changes: they are photographs of a
 * running install. The routing, meters and sources shots are taken from the
 * sports scene, which is seeded from scripts/demo-seed.sports.fixture.json, so
 * that fixture's first programme IS the layout, this file restates it for the
 * site, and src/lib/features-page.test.ts fails if the two drift apart.
 *
 * `fixtureLabel` is the label the fixture gives the track, quoted exactly; it
 * is what the routing and meters screenshots print. `dbfs` is a nominal level,
 * not a measurement.
 */
export const TRACK_LAYOUT = [
  { id: 1, name: "English", fixtureLabel: "English", note: "commentary, en", dbfs: -23, slug: "english" },
  { id: 2, name: "Español", fixtureLabel: "Español", note: "commentary, es", dbfs: -23, slug: "espanol" },
  { id: 3, name: "Português", fixtureLabel: "Português", note: "commentary, pt", dbfs: -23, slug: "portugues" },
  { id: 4, name: "Français", fixtureLabel: "Français", note: "commentary, fr", dbfs: -23, slug: "francais" },
  { id: 5, name: "Arabic", fixtureLabel: "Arabic", note: "commentary, ar", dbfs: -23, slug: "arabic" },
  { id: 6, name: "Crowd", fixtureLabel: "Crowd", note: "crowd and stadium", dbfs: -26, slug: "crowd" },
];
