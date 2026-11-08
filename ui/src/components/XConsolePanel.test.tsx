// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { XConsolePanel } from "./XConsolePanel";
import en from "@/lib/i18n/en.json";

// The X console in the destination dialog (#3). It says whether the Livestream gate is
// open, where the X broadcast stands, and holds the go-live title, which X takes only
// when the broadcast is published. While the broadcast is live the title is read-only,
// because X has no write that changes it.

afterEach(() => {
  cleanup();
});

const noop = () => undefined;

describe("the X console", () => {
  it("names the X Livestream switch the operator needs while the gate is closed", () => {
    render(<XConsolePanel gate="closed" title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    expect(screen.getByText(en["wave3.x.gateClosed"])).toBeTruthy();
    expect(screen.queryByText(en["wave3.x.gateOpen"])).toBeNull();
  });

  it("says what polyemesis does once the gate is open", () => {
    render(<XConsolePanel gate="open" title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    expect(screen.getByText(en["wave3.x.gateOpen"])).toBeTruthy();
  });

  it("shows the broadcast's state in the coordinator's words", () => {
    const { rerender } = render(
      <XConsolePanel gate="open" lifecycle={{ broadcastId: "bc-1", phase: "created" }} title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />,
    );
    expect(screen.getByText(en["wave3.x.status.created"])).toBeTruthy();
    rerender(
      <XConsolePanel gate="open" lifecycle={{ broadcastId: "bc-1", phase: "live" }} title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />,
    );
    expect(screen.getByText(en["wave3.x.status.live"])).toBeTruthy();
    rerender(<XConsolePanel gate="open" lifecycle={{ phase: "complete" }} title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    expect(screen.getByText(en["wave3.x.status.ended"])).toBeTruthy();
    rerender(<XConsolePanel gate="open" title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    expect(screen.getByText(en["wave3.x.status.none"])).toBeTruthy();
  });

  it("edits the go-live title before the show and passes the change up", () => {
    const onTitleChange = vi.fn();
    render(<XConsolePanel gate="open" title="Sunday" onTitleChange={onTitleChange} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    const input = screen.getByLabelText(en["wave3.x.titleLabel"]) as HTMLInputElement;
    expect(input.disabled).toBe(false);
    expect(input.maxLength).toBe(280);
    fireEvent.change(input, { target: { value: "Sunday practice" } });
    expect(onTitleChange).toHaveBeenCalledWith("Sunday practice");
  });

  it("makes the title read-only while the X broadcast is live, and says why", () => {
    render(
      <XConsolePanel gate="open" lifecycle={{ broadcastId: "bc-1", phase: "live" }} title="Sunday" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />,
    );
    const input = screen.getByLabelText(en["wave3.x.titleLabel"]) as HTMLInputElement;
    expect(input.disabled).toBe(true);
    expect(screen.getByText(en["wave3.x.titleLocked"])).toBeTruthy();
  });

  it("keeps the title read-only while X is publishing, because the publish already carried it", () => {
    render(
      <XConsolePanel
        gate="open"
        lifecycle={{ broadcastId: "bc-1", phase: "prepublished" }}
        title="Sunday"
        onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop}
      />,
    );
    expect((screen.getByLabelText(en["wave3.x.titleLabel"]) as HTMLInputElement).disabled).toBe(true);
  });

  it("shows the platform's fault next to the state", () => {
    render(
      <XConsolePanel
        gate="open"
        lifecycle={{ broadcastId: "bc-1", phase: "created", fault: "X refused" }}
        title=""
        onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop}
      />,
    );
    expect(screen.getByText(en["wave3.x.status.fault"].replace("{fault}", "X refused"))).toBeTruthy();
  });

  it("offers who can chat and the language, both left to X by default", () => {
    render(<XConsolePanel gate="open" title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={null} onControlsChange={noop} />);
    const chat = screen.getByLabelText(en["wave3.x.chatLabel"]);
    const lang = screen.getByLabelText(en["wave3.x.localeLabel"]);
    expect(chat.textContent).toBe(en["wave3.x.unset"]);
    expect(lang.textContent).toBe(en["wave3.x.unset"]);
  });

  it("shows the stored chat mode and language, even a language outside the list", () => {
    render(
      <XConsolePanel gate="open" title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={{ chatOption: 4, locale: "xx-YY" }} onControlsChange={noop} />,
    );
    expect(screen.getByLabelText(en["wave3.x.chatLabel"]).textContent).toBe(en["wave3.x.chat.4"]);
    expect(screen.getByLabelText(en["wave3.x.localeLabel"]).textContent).toContain("xx-YY");
  });

  it("locks the chat mode and language while the X broadcast is live", () => {
    render(
      <XConsolePanel gate="open" lifecycle={{ broadcastId: "bc-1", phase: "live" }} title="" onTitleChange={noop} tweet="first" onTweetChange={noop} controls={{ chatOption: 2 }} onControlsChange={noop} />,
    );
    expect((screen.getByLabelText(en["wave3.x.chatLabel"]) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText(en["wave3.x.localeLabel"]) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(en["wave3.x.controlsLocked"])).toBeTruthy();
  });
});
