import { describe, expect, it } from "vitest";

import { cn } from "./utils";

/* A type-scale step is a size, not a colour (B2). */
describe("cn", () => {
  it("keeps the text colour beside a custom size step", () => {
    expect(cn("bg-primary text-primary-foreground", "h-7 text-tiny")).toBe("bg-primary text-primary-foreground h-7 text-tiny");
    expect(cn("text-down", "text-micro")).toBe("text-down text-micro");
  });

  it("still lets a later size replace an earlier one", () => {
    expect(cn("text-sm", "text-tiny")).toBe("text-tiny");
    expect(cn("text-display", "text-lg")).toBe("text-lg");
  });
});
