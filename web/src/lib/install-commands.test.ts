import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { BUILD_FROM_CLONE, CLONE, COMPOSE_UP, DOCKER_RUN, HEALTH_CHECK, INSTALL_SCRIPT, SETUP_CODE } from "../data/install.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const readme = readFileSync(join(here, "..", "..", "..", "README.md"), "utf8");
const doc = (name: string) => readFileSync(join(here, "..", "..", "..", "docs", name), "utf8");

describe("install commands", () => {
  it("runs the published image with every port", () => {
    expect(DOCKER_RUN).toContain("rainmanjam/polyemesis:latest");
    expect(DOCKER_RUN).toContain("-p 8080:8080 -p 6000:6000/udp -p 1935:1935");
  });

  it("matches the docker run command in docs/QUICKSTART.md", () => expect(doc("QUICKSTART.md")).toContain(DOCKER_RUN));

  it("matches the scripted install in docs/INSTALL.md", () => {
    const install = doc("INSTALL.md");
    const end = "sudo bash install.sh";
    const script = install.slice(install.indexOf("curl --proto"), install.indexOf(end) + end.length);
    expect(INSTALL_SCRIPT).toBe(script);
  });

  it("brings Compose up with one command", () => {
    expect(COMPOSE_UP).toBe("docker compose up -d");
    expect(COMPOSE_UP).not.toContain("git clone");
  });

  it("clones before it brings Compose up, as docs/INSTALL.md does", () => {
    expect(CLONE).toBe("git clone https://github.com/rainmanjam/polyemesis\ncd polyemesis");
    expect(doc("INSTALL.md")).toContain(`${CLONE}\n${COMPOSE_UP}`);
  });

  it("builds from a clone as the README's Try it does", () => {
    expect(readme).toContain(BUILD_FROM_CLONE);
  });

  it("checks health with the two lines docs/INSTALL.md gives", () => {
    for (const line of HEALTH_CHECK.split("\n")) expect(doc("INSTALL.md")).toContain(line);
  });

  it("reads the setup code as docs/INSTALL.md says for docker run", () => {
    for (const line of SETUP_CODE.split("\n")) expect(doc("INSTALL.md")).toContain(line);
  });
});
