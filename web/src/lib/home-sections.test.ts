import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { COMPETITORS } from "./competitors";
import { CLONE, COMPOSE_UP, DOCKER_RUN, INSTALL_SCRIPT } from "../data/install.mjs";

/* Source-text guards for the Why, Install and Use sections (stages 03 to 05). */

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, "..", ...p), "utf8");
const why = read("components", "home", "Why.astro");
const install = read("components", "home", "Install.astro");
const use = read("components", "home", "Use.astro");
const stripComments = (t: string) => t.replace(/\/\*[\s\S]*?\*\//g, "");
const sections = { why, install, use };

describe("install section", () => {
  it("is the install-stage section with its id and script hook", () => {
    expect(install).toMatch(/<section[^>]*id="install"/);
    expect(install).toMatch(/<section[^>]*data-install/);
    expect(install).toMatch(/data-journey-stage="install"/);
  });

  it("types no FFmpeg version and writes the Docker Engine floor once", () => {
    expect(install).toMatch(/FFMPEG_FLOOR/);
    expect(stripComments(install)).not.toMatch(/\b8\.0\b/);
    expect(stripComments(install).match(/20\.10/g)).toHaveLength(1);
  });

  it("takes every command from install.mjs and holds no command literal", () => {
    expect(install).toMatch(/from "\.\.\/\.\.\/data\/install\.mjs"/);
    const markup = install.slice(install.indexOf("---", 3) + 3);
    const body = install.slice(0, install.indexOf("---", 3) + 3);
    for (const name of ["DOCKER_RUN", "CLONE", "COMPOSE_UP", "INSTALL_SCRIPT"]) {
      // imported once, and used beyond the import line
      expect(body.match(new RegExp(`\\b${name}\\b`, "g"))!.length, name).toBeGreaterThanOrEqual(2);
    }
    expect(markup).toMatch(/<CodeBlock/);
    for (const cmd of [DOCKER_RUN, CLONE, COMPOSE_UP, INSTALL_SCRIPT]) {
      for (const line of cmd.split("\n").map((l) => l.trim().replace(/ \\$/, ""))) {
        if (line) expect(install, `a command line typed into Install.astro: ${line}`).not.toContain(line);
      }
    }
    expect(install).not.toMatch(/^\s*(\$ )?(docker run|git clone|curl|sudo bash)\b/m);
  });

  it("has the docker, compose and script tabs, one CodeBlock per panel", () => {
    for (const key of ["docker", "compose", "script"]) {
      expect(install).toContain(`"${key}"`);
    }
    expect(install).toMatch(/role="tablist"/);
    expect(install).toMatch(/import CodeBlock from "\.\.\/CodeBlock\.astro"/);
    // one CodeBlock rendered once per method, over exactly the three methods
    expect(install.match(/<CodeBlock\b/g)).toHaveLength(1);
    expect(install.match(/^\s+key: "/gm)).toHaveLength(3);
  });

  it("links to /download from .more", () => {
    expect(install).toMatch(/class="more"[^>]*href="\/download"|href="\/download"[^>]*class="more"/);
  });
});

describe("why section", () => {
  it("is the why-stage section with its id", () => {
    expect(why).toMatch(/<section[^>]*id="why"/);
    expect(why).toMatch(/data-journey-stage="why"/);
  });

  it("has no table header named capability (the build treats that as the comparison table)", () => {
    const first = /<th\b[^>]*>([\s\S]*?)<\/th>/i.exec(why);
    expect(first).not.toBeNull();
    expect(first![1].replace(/<[^>]+>/g, "").trim().toLowerCase()).not.toBe("capability");
  });

  it("names no other product", () => {
    expect(why).not.toMatch(COMPETITORS);
    expect(why).not.toMatch(/datarhei|Restreamer/i);
  });

  it("reads the FFmpeg floor, release and screenshots from their sources and types no 8.0", () => {
    expect(why).toMatch(/FFMPEG_FLOOR/);
    expect(why).toMatch(/latestRelease\(\)/);
    expect(why).toMatch(/assets\/shots\/18-destinations/);
    expect(why).toMatch(/assets\/shots\/07-monitoring/);
    expect(stripComments(why)).not.toMatch(/\b8\.0\b/);
  });

  it("keeps the common questions as native details elements", () => {
    expect(why).toMatch(/<details\b/);
    expect(why.match(/^\s+"[^"]+\?",$/gm)?.length ?? 0).toBeGreaterThanOrEqual(4);
  });
});

describe("use section", () => {
  it("is the use-stage section with its id", () => {
    expect(use).toMatch(/<section[^>]*id="use"/);
    expect(use).toMatch(/data-journey-stage="use"/);
  });

  it("closes on Install polyemesis, linked to /download", () => {
    expect(use).toMatch(/<a[^>]*class="btn btn-primary"[^>]*href="\/download"[^>]*>\s*Install polyemesis\s*<\/a>/);
  });

  it("counts the docs instead of typing the number", () => {
    expect(use).toMatch(/PUBLISHED\.length/);
    expect(use).not.toMatch(/\b\d+ pages\b/);
  });

  it("does not link to pages that a later plan creates", () => {
    expect(use).not.toMatch(/href="\/(recipes|for|tour)\b/);
  });
});

describe("all three", () => {
  for (const [name, src] of Object.entries(sections)) {
    it(`${name}: no empty link, no entrance animation, no banned copy`, () => {
      expect(src).not.toContain('href="#"');
      expect(src).not.toMatch(/\breveal\b|data-reveal/);
      // "Stream Deck +" never breaks across a line: a no-break space (written
      // \u00a0 in a string, or literal) between the name and the plus.
      for (const m of src.matchAll(/Stream Deck(\\u00a0|\u00a0|&nbsp;| |)\+/g)) {
        expect(m[1], "Stream Deck + needs a no-break space").not.toMatch(/^( |)$/);
      }
      expect(src).not.toMatch(/three steps|five minutes|one maintainer|pre-release|bypass|dodge|DMCA|Stream Deck\+/i);
    });
  }
});
