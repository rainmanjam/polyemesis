// @vitest-environment jsdom
//
// THE RUNNING VERSION WAS SHOWN NOWHERE ON A HEALTHY INSTALL.
//
// UpdateBanner is the only component that reads info.version, and it returns
// null unless an update is available or the build is a development one. The
// normal state -- an up-to-date release -- therefore displayed no version at
// all, and answering "which version is this?" meant SSH and `polyemesis
// -version`. The data was already there; only the display was missing. #660.

import { afterEach, describe, it, expect, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { VersionTag } from "./VersionTag";
import type { VersionInfo } from "@/lib/types";

vi.mock("@/lib/i18n", () => ({
  useT: () => (key: string, vars?: Record<string, unknown>) =>
    `${key}:${JSON.stringify(vars ?? {})}`,
}));

afterEach(cleanup);

const info = (over: Partial<VersionInfo>): VersionInfo =>
  ({ version: "v0.9.0", updateAvailable: false, comparable: true, ...over }) as VersionInfo;

describe("the chrome's version tag", () => {
  it("shows the version when the install is current and no banner is showing", () => {
    // The whole bug in one assertion: this is the state in which nothing used
    // to be displayed.
    render(<VersionTag info={info({ updateAvailable: false })} />);
    expect(screen.getByText("v0.9.0")).toBeTruthy();
  });

  it("shows a development build as-is", () => {
    // "Which build is this" is the question being answered, and a git-describe
    // string is a legitimate answer to it rather than something to hide.
    render(<VersionTag info={info({ version: "v0.9.0-12-gabc1234", comparable: false })} />);
    expect(screen.getByText("v0.9.0-12-gabc1234")).toBeTruthy();
  });

  it("renders nothing before the answer arrives", () => {
    // Not a placeholder. An em-dash where a version belongs invites a reader to
    // conclude something about their install from a fact nobody has stated --
    // which is #663 in miniature.
    const { container } = render(<VersionTag info={null} />);
    expect(container.textContent).toBe("");
  });

  it("renders nothing when the server reported no version string", () => {
    const { container } = render(<VersionTag info={info({ version: "" })} />);
    expect(container.textContent).toBe("");
  });
});
