/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { liveSocketUrl } from "./liveSocket";

/* Every socket the console opens names its programme, and the way that is kept
 * true is that there is exactly one way to build the URL. The chat socket was
 * the second hand-rolled copy of LiveDataProvider's URL, and the copy left out
 * `?source=` -- so this scans for any third copy rather than trusting review to
 * notice one. */

const SRC = fileURLToPath(new URL("..", import.meta.url));

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sources(path);
    if (!/\.(ts|tsx)$/.test(name) || /\.test\.(ts|tsx)$/.test(name)) return [];
    return [path];
  });
}

describe("liveSocketUrl", () => {
  beforeEach(() => {
    vi.stubGlobal("location", { protocol: "https:", host: "console.example" });
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("names the programme when there is one", () => {
    expect(liveSocketUrl(7)).toBe("wss://console.example/api/v1/ws?source=7");
  });

  it("sends no source on an install that has none", () => {
    expect(liveSocketUrl(null)).toBe("wss://console.example/api/v1/ws");
  });

  it("is the only way src/ opens a WebSocket", () => {
    const offenders = sources(SRC).filter((file) => {
      const text = readFileSync(file, "utf8");
      const opens = text.match(/new WebSocket\(([^)]*)\)/g) ?? [];
      return opens.some((call) => !call.includes("liveSocketUrl("));
    });
    expect(offenders.map((f) => relative(SRC, f))).toEqual([]);
  });
});
