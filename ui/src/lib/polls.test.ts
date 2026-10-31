import { describe, expect, it } from "vitest";
import { clock, exportHref, isLive, platformsByVotes, pollProblem, secondsLeft, sharePercents } from "./polls";

const ok = { question: "Next map?", options: ["Dust", "Mirage"], durationSeconds: 60 };

describe("the poll form's checks match the server's limits", () => {
  it("accepts a valid poll and names the first problem otherwise", () => {
    expect(pollProblem(ok)).toBeNull();
    expect(pollProblem({ ...ok, question: "  " })).toBe("polls.errQuestion");
    expect(pollProblem({ ...ok, question: "q".repeat(101) })).toBe("polls.errQuestionLong");
    // Characters, not UTF-16 units: 100 emoji is 100 characters.
    expect(pollProblem({ ...ok, question: "🎮".repeat(100) })).toBeNull();
    expect(pollProblem({ ...ok, options: ["a"] })).toBe("polls.errOptionCount");
    expect(pollProblem({ ...ok, options: ["a", "b", "c", "d", "e", "f", "g"] })).toBe("polls.errOptionCount");
    expect(pollProblem({ ...ok, options: ["a", " "] })).toBe("polls.errOptionEmpty");
    expect(pollProblem({ ...ok, options: ["a", "b".repeat(31)] })).toBe("polls.errOptionLong");
    expect(pollProblem({ ...ok, options: ["Yes", " yes "] })).toBe("polls.errOptionRepeat");
    expect(pollProblem({ ...ok, durationSeconds: 14 })).toBe("polls.errDuration");
    expect(pollProblem({ ...ok, durationSeconds: 3601 })).toBe("polls.errDuration");
    expect(pollProblem({ ...ok, durationSeconds: 30.5 })).toBe("polls.errDuration");
  });
});

describe("the numbers the panel draws", () => {
  it("shares, time left, clock, ordering, liveness and export links", () => {
    expect(sharePercents({ total: 3, options: [{ label: "a", votes: 2, byPlatform: {} }, { label: "b", votes: 1, byPlatform: {} }] }))
      .toEqual([67, 33]);
    expect(sharePercents({ total: 0, options: [{ label: "a", votes: 0, byPlatform: {} }] })).toEqual([0]);
    expect(secondsLeft({ endsAt: new Date(10_000).toISOString() }, 8_500)).toBe(2);
    expect(secondsLeft({ endsAt: "x" }, 0)).toBe(0);
    expect(clock(65)).toBe("1:05");
    expect(platformsByVotes({ youtube: 2, twitch: 2, kick: 5, rumble: 0 })).toEqual([["kick", 5], ["twitch", 2], ["youtube", 2]]);
    expect(isLive(null)).toBe(false);
    expect(isLive({ state: "closing" } as never)).toBe(true);
    expect(isLive({ state: "ended" } as never)).toBe(false);
    expect(exportHref()).toBe("/api/v1/polls/export");
    expect(exportHref(4)).toBe("/api/v1/polls/export?id=4");
  });
});
