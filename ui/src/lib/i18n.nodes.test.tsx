// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import { interpolateNodes } from "./i18n";

describe("interpolateNodes", () => {
  it("puts nodes where the translation puts the slot, not where English did", () => {
    const { container } = render(
      <p>{interpolateNodes("このミックスは{b}、どのプラットフォームにも。", { b: <strong>まだ送信されていません</strong> })}</p>,
    );
    expect(container.innerHTML).toBe("<p>このミックスは<strong>まだ送信されていません</strong>、どのプラットフォームにも。</p>");
  });

  it("accepts strings and numbers in slots and leaves an unknown slot as written", () => {
    const { container } = render(<p>{interpolateNodes("{n} of {total} in {x}", { n: 2, total: "5" })}</p>);
    expect(container.textContent).toBe("2 of 5 in {x}");
  });

  it("repeats a slot used twice", () => {
    const { container } = render(<p>{interpolateNodes("{a} and {a}", { a: <em>x</em> })}</p>);
    expect(container.querySelectorAll("em")).toHaveLength(2);
  });
});
