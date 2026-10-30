import { describe, expect, it } from "vitest";

import { fetchRepoStats } from "./github-stats";

/* The build reads GitHub once and prints what it read. These pin the one
 * property that matters: when GitHub is unreachable, rate-limited or returns
 * something unexpected, the page gets NULL, so it prints no number at all. A
 * fabricated or stale-looking star count is the failure this exists to stop. */

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("fetchRepoStats", () => {
  it("returns the star count and the latest release tag it was given", async () => {
    const fetchImpl = async (url: string | URL | Request) => {
      const u = String(url);
      if (u.endsWith("/releases/latest")) return jsonResponse({ tag_name: "v0.9.1" });
      return jsonResponse({ stargazers_count: 42 });
    };
    await expect(fetchRepoStats(fetchImpl as typeof fetch)).resolves.toEqual({
      stars: 42,
      release: "v0.9.1",
    });
  });

  it("keeps the stars and reports no release when the repo has no release yet", async () => {
    const fetchImpl = async (url: string | URL | Request) =>
      String(url).endsWith("/releases/latest")
        ? jsonResponse({ message: "Not Found" }, 404)
        : jsonResponse({ stargazers_count: 7 });
    await expect(fetchRepoStats(fetchImpl as typeof fetch)).resolves.toEqual({
      stars: 7,
      release: null,
    });
  });

  it("returns null when GitHub refuses the repo request (rate limit, outage)", async () => {
    const fetchImpl = async () => jsonResponse({ message: "rate limited" }, 403);
    await expect(fetchRepoStats(fetchImpl as typeof fetch)).resolves.toBeNull();
  });

  it("returns null when the network throws, rather than failing the build", async () => {
    const fetchImpl = async () => {
      throw new TypeError("fetch failed");
    };
    await expect(fetchRepoStats(fetchImpl as typeof fetch)).resolves.toBeNull();
  });

  it("returns null when the star count is not a number", async () => {
    const fetchImpl = async (url: string | URL | Request) =>
      String(url).endsWith("/releases/latest")
        ? jsonResponse({ tag_name: "v1.0.0" })
        : jsonResponse({ stargazers_count: "lots" });
    await expect(fetchRepoStats(fetchImpl as typeof fetch)).resolves.toBeNull();
  });
});
