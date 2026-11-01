import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { classifyRun, classifySkip } from "./recurringOutcome";

describe("classifyRun", () => {
  it("reads no error as a rollover that finished", () => {
    expect(classifyRun(null)).toEqual({ kind: "rolled" });
  });
  it("reads 409 as a refusal the operator can act on", () => {
    expect(classifyRun(new ApiError(409, "too soon", ""))).toEqual({ kind: "refused" });
  });
  it("reads 502 as the platform answering and refusing", () => {
    expect(classifyRun(new ApiError(502, "YouTube refused", ""))).toEqual({ kind: "platform" });
  });
  it("reads 404 as a destination that no longer exists", () => {
    expect(classifyRun(new ApiError(404, "no such destination", ""))).toEqual({ kind: "missing" });
  });
  it("reads 500 as polyemesis failing", () => {
    expect(classifyRun(new ApiError(500, "boom", ""))).toEqual({ kind: "server" });
  });
  it("keeps any other status visible rather than folding it into a known one", () => {
    expect(classifyRun(new ApiError(418, "teapot", ""))).toEqual({ kind: "other", status: 418 });
  });
  it("treats a non-HTTP failure as a server fault", () => {
    expect(classifyRun(new Error("network down"))).toEqual({ kind: "server" });
  });
});

describe("classifySkip", () => {
  it("reads no error as skipped", () => {
    expect(classifySkip(null)).toEqual({ kind: "skipped" });
  });
  it("reads 409 as refused, not as a failed platform call", () => {
    expect(classifySkip(new ApiError(409, "started", ""))).toEqual({ kind: "refused" });
  });
  it("reads 502 as the platform refusing the cancel", () => {
    expect(classifySkip(new ApiError(502, "cancel failed", ""))).toEqual({ kind: "platform" });
  });
  it("reads anything else as a plain failure", () => {
    expect(classifySkip(new ApiError(500, "boom", ""))).toEqual({ kind: "failed" });
  });
});
