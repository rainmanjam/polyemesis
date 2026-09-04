// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { useLiveData } from "@/hooks/useLiveData";
import { LiveDataProvider } from "./LiveDataProvider";
import { api } from "@/lib/api";

/* #638: THE OPERATOR'S CHOICE, WHICH NOTHING COULD MAKE.
 *
 * rememberProgramme was called from exactly one place, with the value
 * resolveProgramme had just picked. The persistence worked; it only ever
 * persisted the fallback. This pins the other direction — an operator's choice
 * takes effect, survives a reload, and a programme the server does not list is
 * refused rather than stored.
 *
 * That last one matters more than it looks: a remembered ghost id is sent on
 * every poll, the server answers 409 for a programme that does not exist, and
 * the operator gets a dead console with nothing on screen to explain it. It
 * survives reloads, which is exactly what makes it hard to diagnose.
 */

const list = vi.spyOn(api, "listSources");
const srcOf = vi.spyOn(api, "source");

function Probe() {
  const { programme, programmes, selectProgramme } = useLiveData();
  return (
    <div>
      <span data-testid="programme">{String(programme)}</span>
      <span data-testid="names">{programmes.map((p) => p.name).join(",")}</span>
      <button onClick={() => selectProgramme(2)}>pick 2</button>
      <button onClick={() => selectProgramme(99)}>pick a ghost</button>
    </div>
  );
}

const rows = (...ids: number[]) => ids.map((id) => ({ id, name: `s${id}` })) as never;

function draw() {
  return render(
    <LiveDataProvider>
      <Probe />
    </LiveDataProvider>,
  );
}

beforeEach(() => {
  list.mockReset();
  srcOf.mockReset();
  srcOf.mockResolvedValue(null as never);
  try {
    localStorage.clear();
  } catch {
    /* private mode */
  }
});
afterEach(cleanup);

describe("LiveDataProvider: choosing a programme", () => {
  it("exposes every programme by name, so a switcher need not ask again", async () => {
    list.mockResolvedValue(rows(1, 2));
    draw();
    await waitFor(() => expect(screen.getByTestId("names").textContent).toBe("s1,s2"));
  });

  it("follows the operator's choice and remembers it across a reload", async () => {
    list.mockResolvedValue(rows(1, 2));
    draw();
    // The fallback: the server's first source.
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("1"));

    await act(async () => {
      screen.getByText("pick 2").click();
    });
    expect(screen.getByTestId("programme").textContent).toBe("2");

    // A fresh mount is a reload. Before #638 this came back as 1 forever,
    // because the only thing ever written was the resolver's own pick.
    cleanup();
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("2"));
  });

  it("refuses a programme the server does not list", async () => {
    list.mockResolvedValue(rows(1, 2));
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("1"));

    await act(async () => {
      screen.getByText("pick a ghost").click();
    });
    expect(screen.getByTestId("programme").textContent).toBe("1");

    // And it must not have been written: a stored ghost survives the reload
    // and produces a 409 on every poll, which reads as a dead console.
    cleanup();
    draw();
    await waitFor(() => expect(screen.getByTestId("programme").textContent).toBe("1"));
  });
});
