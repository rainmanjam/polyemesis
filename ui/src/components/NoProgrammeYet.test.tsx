// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import { NoProgrammeYet } from "./NoProgrammeYet";
import { translate } from "@/lib/i18n";

/* app-council A83. The no-programme state is drawn by the kit's EmptyState, so
 * it is the same shape as every other "nothing here yet" on the console, and
 * its one action is a real button rather than a styled link. */

afterEach(cleanup);

describe("NoProgrammeYet", () => {
  it("is an EmptyState whose default action goes to Sources", () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <Routes>
          <Route path="/" element={<NoProgrammeYet title="No programme yet" body="Create one." />} />
          <Route path="/sources" element={<p>sources page</p>} />
        </Routes>
      </MemoryRouter>,
    );
    const title = screen.getByText("No programme yet");
    expect(title.closest(".border-dashed")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: translate("en", "empty.goToSources") }));
    expect(screen.getByText("sources page")).toBeTruthy();
  });

  it("uses the caller's action instead, for the Sources page itself", () => {
    const onClick = vi.fn();
    render(
      <MemoryRouter>
        <NoProgrammeYet title="No sources yet" body="Add one." action={{ label: "Add source", onClick }} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Add source" }));
    expect(onClick).toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: translate("en", "empty.goToSources") })).toBeNull();
  });
});
