import { describe, expect, it } from "vitest";
import { ivsAppPathFix } from "./ivsIngest";

/* The same cases as services_test.go's IVS tests, so the dialog's hint and the
 * server's refusal cannot disagree about which URLs they mean. */
const host = "fa723fc1b171.global-contribute.live-video.net";

describe("ivsAppPathFix", () => {
  it.each([
    ["Kick's dashboard, verbatim", `rtmps://${host}/`, `rtmps://${host}:443/app`],
    ["no trailing slash", `rtmps://${host}`, `rtmps://${host}:443/app`],
    ["port kept", `rtmps://${host}:443`, `rtmps://${host}:443/app`],
    ["plain rtmp keeps its scheme", `rtmp://${host}/`, `rtmp://${host}/app`],
    ["key pasted as the path", `rtmps://${host}:443/sk_us-west-2_placeholder`, `rtmps://${host}:443/app`],
    ["another application", `rtmps://${host}/live`, `rtmps://${host}:443/app`],
  ])("catches %s and offers the working URL", (_name, input, fix) => {
    expect(ivsAppPathFix(input)).toBe(fix);
  });

  it.each([
    `rtmps://${host}:443/app`,
    `rtmps://${host}/app/`,
    "rtmps://ingest.global-contribute.live-video.net/app/sk_placeholder",
    "rtmps://evilglobal-contribute.live-video.net/",
    "rtmps://global-contribute.live-video.net/",
    "rtmps://live.twitch.tv/",
    `srt://${host}:9000`,
    "",
  ])("leaves %s alone", (input) => {
    expect(ivsAppPathFix(input)).toBeNull();
  });
});

describe("ivsAppPathFix on pathological input", () => {
  it("finishes fast on a huge run of slashes and still answers", () => {
    const t0 = performance.now();
    expect(ivsAppPathFix(`rtmps://${host}/${"/".repeat(200_000)}x/y`)).toBe(`rtmps://${host}:443/app`);
    expect(ivsAppPathFix(`rtmps://${host}/${"/".repeat(200_000)}app`)).toBeNull();
    expect(performance.now() - t0).toBeLessThan(250);
  });
});
