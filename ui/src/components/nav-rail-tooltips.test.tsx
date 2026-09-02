// @vitest-environment jsdom
//
// COLLAPSING THE RAIL USED TO PAINT A LABEL FOR EVERY LINK THE POINTER HAD
// CROSSED.
//
// Radix opens a Tooltip.Root on hover whether or not that Root has any content
// to render. The expanded nav deliberately renders no TooltipContent -- the
// label is already on screen, and a tooltip repeating it is noise that also
// delays every hover -- so a hover while expanded set open=true on a Root with
// nothing to show. Invisible, and nothing closed it.
//
// Collapse, TooltipContent mounts, and every Root still holding that state
// painted at once: a column of labels over the page, one per link the pointer
// had happened to cross, and none for the links it had not. Hovering any of
// them cleared that one, because a real pointer cycle finally delivered a
// close -- which made it read like a rendering glitch rather than state.
//
// The fix controls `open` from AppLayout, so while expanded the expression is
// false for every link and no hover can leave anything to resurface. This test
// is the reproduction that found it, kept.

import { afterEach, describe, it, expect } from "vitest";
import { cleanup, render, screen, fireEvent } from "@testing-library/react";
import { useState } from "react";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

const ITEMS = ["Dashboard", "Sources", "Routing"];

/** The rail's tooltip wiring, kept structurally identical to AppLayout's. */
function Rail({ collapsed }: { collapsed: boolean }) {
  const [openTip, setOpenTip] = useState<string | null>(null);
  const [wasCollapsed, setWasCollapsed] = useState(collapsed);
  if (wasCollapsed !== collapsed) {
    setWasCollapsed(collapsed);
    setOpenTip(null);
  }
  return (
    <TooltipProvider delayDuration={0}>
      <nav>
        {ITEMS.map((t) => (
          <Tooltip
            key={t}
            open={collapsed && openTip === t}
            onOpenChange={(open) => setOpenTip((prev) => (open ? t : prev === t ? null : prev))}
          >
            <TooltipTrigger asChild>
              <a href={"/" + t} aria-label={t}>
                <span className={collapsed ? "md:hidden" : ""}>{t}</span>
              </a>
            </TooltipTrigger>
            {collapsed && (
              <TooltipContent side="right" aria-hidden>
                {t}
              </TooltipContent>
            )}
          </Tooltip>
        ))}
      </nav>
    </TooltipProvider>
  );
}

/** A label appears twice when its tooltip is open: once in the link, once in
 *  the tooltip. Counting occurrences is what distinguishes the two states
 *  without depending on Radix's internal markup. */
// Not configured globally in this project, and without it the first test's DOM
// survives into the second -- where "found multiple elements" reads exactly
// like the fix having broken something.
afterEach(cleanup);

const openTips = () => ITEMS.filter((t) => screen.queryAllByText(t).length > 1);

describe("the collapsed nav rail's tooltips", () => {
  it("opens none of them just because links were hovered before collapsing", async () => {
    const { rerender } = render(<Rail collapsed={false} />);

    // Browsing the expanded nav the way anyone does: the pointer crosses
    // several links on its way somewhere.
    for (const t of ITEMS) {
      const link = screen.getByRole("link", { name: t });
      fireEvent.pointerMove(link, { pointerType: "mouse" });
      fireEvent.focus(link);
    }

    rerender(<Rail collapsed={true} />);
    await new Promise((r) => setTimeout(r, 50));

    expect(
      openTips(),
      "collapsing painted a tooltip for every link the pointer had crossed while the rail was expanded",
    ).toEqual([]);
  });

  it("still opens the one tooltip a pointer is actually on", async () => {
    // The control. A fix that simply never opened anything would pass the test
    // above and leave the collapsed rail as unnamed icons, which is the whole
    // reason the tooltips exist.
    render(<Rail collapsed={true} />);
    // pointerMove, not pointerEnter: Radix's trigger listens on pointermove and
    // focus. Firing the wrong one makes a working tooltip look broken, which
    // cost a wrong diagnosis of the fix above before this comment existed.
    const link = screen.getByRole("link", { name: "Sources" });
    fireEvent.pointerMove(link, { pointerType: "mouse" });
    fireEvent.focus(link);
    await new Promise((r) => setTimeout(r, 50));

    expect(openTips()).toEqual(["Sources"]);
  });
});
