import { describe, expect, it, vi } from "vitest";

/* astro:assets is a virtual module that exists only inside an Astro build, so
 * it is replaced here with a stand-in that records what it was asked for. */
const getImage = vi.fn(async (opts: { width: number; format: string }) => ({
  src: `/_astro/shot_${opts.width}.${opts.format}`,
}));
vi.mock("astro:assets", () => ({ getImage }));

const { lqipStyle } = await import("./lqip");

describe("the screenshot placeholder", () => {
  const shot = { src: "/shot.png", width: 2880, height: 1800, format: "png" } as never;

  it("asks for a 32px-wide webp of the same capture", async () => {
    await lqipStyle(shot);
    expect(getImage).toHaveBeenCalledWith(expect.objectContaining({ src: shot, width: 32, format: "webp" }));
  });

  /* A same-origin file, never a data: URI: a base64 blob per screenshot would
   * be a high-entropy literal in every built page. */
  it("paints that file as a cover background, by URL", async () => {
    const style = await lqipStyle(shot);
    expect(style).toContain("background-image:url(/_astro/shot_32.webp)");
    expect(style).toContain("background-size:cover");
    expect(style).not.toMatch(/data:/);
  });
});
