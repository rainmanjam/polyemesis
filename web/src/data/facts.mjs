// @ts-check
/* Facts the site states about the server, in one place.
 *
 * facts.json is written by the Go test TestWireContract (internal/api) from the
 * server's own constants: routing.MaxTracks, ffmpeg.MinMajorVersion, go.mod's go
 * directive and ui/package.json's engines. web/scripts/check-build.mjs then
 * checks the file against those sources again and reads every sentence of the
 * site and the README that restates one, so a number cannot move on the server
 * and stay as it was on a page.
 *
 * A page that states one of these imports it from here instead of typing it. */
import facts from "./facts.json";

/** The most audio tracks one ingest carries (routing.MaxTracks). */
export const MAX_TRACKS = facts.maxTracks;

/** The FFmpeg the server needs, as the site words it ("8.0 or later"). The
 *  floor is a major version (ffmpeg.MinMajorVersion). */
export const FFMPEG_FLOOR = `${facts.ffmpegMinMajor}.0`;

/** The Go a build from a clone needs, as go.mod states it ("1.27.0"). */
export const GO_FLOOR = facts.goMinVersion;

/** The Node a build from a clone needs, as the UI's engines range words it
 *  ("20.19+/22.12+"). Throws on a range of another shape, so a changed range is
 *  a failed build and not a page that says something else. */
export const NODE_BUILD_FLOOR = (() => {
  const m = /^\^(\d+\.\d+)\.\d+ \|\| >=(\d+\.\d+)\.\d+$/.exec(facts.uiNodeEngines);
  if (!m) throw new Error(`facts: cannot word the Node range ${JSON.stringify(facts.uiNodeEngines)}`);
  return `${m[1]}+/${m[2]}+`;
})();
