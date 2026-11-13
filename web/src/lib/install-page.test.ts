import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { BUILD_FROM_CLONE, CLONE, COMPOSE_UP, HEALTH_CHECK, INSTALL_WITH_FLAGS, SETUP_CODE } from "../data/install.mjs";
import { FIRST_RUN, PORTS, REQUIREMENTS } from "../data/install-page.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const read = (...p: string[]) => readFileSync(join(here, "..", "..", "..", ...p), "utf8");
const page = read("web", "src", "pages", "download.astro");
const homeInstall = read("web", "src", "components", "home", "Install.astro");
const installDoc = read("docs", "INSTALL.md");
const readme = read("README.md");

describe("the install page takes its commands from install.mjs", () => {
  it("imports every shared command", () => {
    for (const name of ["DOCKER_RUN", "CLONE", "COMPOSE_UP", "INSTALL_SCRIPT", "BUILD_FROM_CLONE", "HEALTH_CHECK"]) {
      expect(page, name).toMatch(new RegExp(`\\b${name}\\b`));
    }
  });

  it("holds no command literal", () => {
    for (const lit of ["docker run", "curl --proto", "make build", "/api/v1/health", "docker compose up"]) {
      expect(page, lit).not.toContain(lit);
    }
  });

  it("does not define or export CLONE or COMPOSE_UP", () => {
    expect(page).not.toMatch(/(const|let|var|export)\s+(CLONE|COMPOSE_UP)\b/);
  });

  it("builds the compose panel from CLONE then COMPOSE_UP", () => {
    expect(page).toContain("`${CLONE}\\n${COMPOSE_UP}`");
  });

  it("agrees with the homepage Install section", () => {
    for (const name of ["DOCKER_RUN", "CLONE", "COMPOSE_UP", "INSTALL_SCRIPT"]) {
      expect(homeInstall, name).toMatch(new RegExp(`\\b${name}\\b`));
    }
  });

  it("fixes COMPOSE_UP and CLONE", () => {
    expect(COMPOSE_UP).toBe("docker compose up -d");
    expect(CLONE).toBe("git clone https://github.com/rainmanjam/polyemesis\ncd polyemesis");
  });

  it("takes every CLONE line from the docs", () => {
    for (const line of CLONE.split("\n").filter(Boolean)) {
      expect(installDoc.includes(line) || readme.includes(line), line).toBe(true);
    }
  });

  it("takes both HEALTH_CHECK lines verbatim from docs/INSTALL.md", () => {
    expect(HEALTH_CHECK.split("\n")).toHaveLength(2);
    for (const line of HEALTH_CHECK.split("\n")) expect(installDoc).toContain(line);
  });

  it("takes BUILD_FROM_CLONE verbatim from the README", () => {
    expect(readme).toContain(BUILD_FROM_CLONE);
    expect(BUILD_FROM_CLONE.startsWith(CLONE)).toBe(true);
  });

  it("shows where installer flags go with the doc's own example", () => {
    expect(installDoc).toContain(INSTALL_WITH_FLAGS);
    expect(page).toContain("INSTALL_WITH_FLAGS");
    expect(page).not.toContain("--mode binary");
  });

  it("links Back up to the doc's before-you-upgrade section", () => {
    expect(page).toContain("/docs/upgrading#before-you-upgrade");
    expect(read("docs", "UPGRADING.md")).toContain("## Before you upgrade");
  });

  it("reads the setup code the way docs/INSTALL.md says for docker run", () => {
    expect(SETUP_CODE).toBe("docker logs polyemesis\ndocker exec polyemesis cat /data/setup-code");
    expect(installDoc).toContain("`docker logs polyemesis`; `docker exec polyemesis cat /data/setup-code`");
  });
});

describe("the install page's facts", () => {
  it("prints the version floors from facts.mjs and types none", () => {
    for (const name of ["FFMPEG_FLOOR", "GO_FLOOR", "NODE_BUILD_FLOOR"]) expect(page, name).toContain(name);
    expect(page).not.toContain("1.27");
  });

  it("keeps no version number in the data", () => {
    for (const r of REQUIREMENTS) expect(`${r.value} ${r.note}`, r.name).not.toMatch(/\b(8\.0|1\.27|20\.19|22\.12)\b/);
  });

  it("lists the ports in order, each in docs/INSTALL.md", () => {
    expect(PORTS.map((p) => p.port)).toEqual(["8080", "6000", "1935", "443", "80"]);
    for (const p of PORTS) expect(installDoc, p.port).toContain(p.port);
    expect(PORTS.find((p) => p.port === "6000")?.proto).toBe("udp");
  });

  it("names Docker Engine 20.10, as docs/INSTALL.md does", () => {
    expect(installDoc).toContain("20.10");
    expect(REQUIREMENTS.find((r) => r.name === "Docker")?.value).toContain("20.10");
  });

  it("lists the seven requirements", () => {
    expect(REQUIREMENTS.map((r) => r.name)).toEqual([
      "Operating system",
      "Docker",
      "FFmpeg without Docker",
      "Build from a clone",
      "CPU",
      "GPU",
      "Bandwidth",
    ]);
  });

  it("points the health check at the install guide's address table", () => {
    expect(page).toContain("POLYEMESIS_URL");
    expect(page).toContain("/docs/install");
  });
});

describe("the install page's structure", () => {
  it("names a real id for every aria-controls", () => {
    const controls = [...page.matchAll(/aria-controls=\{?["`]?([^"`}\s>]+)/g)].map((m) => m[1]);
    expect(controls.length).toBeGreaterThan(0);
    for (const c of controls) expect(page.includes(`id="${c}"`) || page.includes(`id={\`${c}`), c).toBe(true);
  });

  it("has six first-run steps and four distinct screenshots", () => {
    expect(FIRST_RUN).toHaveLength(6);
    const shots = FIRST_RUN.flatMap((s) => (s.shot ? [s.shot.file] : []));
    expect(shots).toEqual(["05-sources.png", "18-destinations.png", "01-dashboard.png"]);
    const used = [...shots, ...[...page.matchAll(/shot="([^"]+\.png)"/g)].map((m) => m[1])];
    expect(new Set(used).size).toBe(4);
    expect(used).toContain("04-meters.png");
  });

  it("carries the tablist, the no-script panels and the HowTo", () => {
    for (const s of ['role="tablist"', "data-js-only", 'id="panel-', "application/ld+json", '"@type": "HowTo"', "PT10M"]) {
      expect(page, s).toContain(s);
    }
    expect(page).toContain("panel-docker");
    expect(page).toContain("panel-binary");
  });

  it("promises no step count or time", () => {
    expect(page).not.toMatch(/three steps/i);
    expect(page).not.toMatch(/first five minutes/i);
    expect(page).not.toMatch(/about (five|two) minutes/i);
  });
});
