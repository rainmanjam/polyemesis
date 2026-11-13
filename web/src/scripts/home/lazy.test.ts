import { afterEach, describe, expect, it, vi } from "vitest";
import { whenNear } from "./lazy";

type Cb = (entries: Array<{ isIntersecting: boolean }>) => void;
let instances: FakeIO[] = [];

class FakeIO {
  disconnected = false;
  margin: string | undefined;
  constructor(
    public cb: Cb,
    opts?: { rootMargin?: string },
  ) {
    this.margin = opts?.rootMargin;
    instances.push(this);
  }
  observe() {}
  unobserve() {}
  disconnect() {
    this.disconnected = true;
  }
  fire(isIntersecting: boolean) {
    if (!this.disconnected) this.cb([{ isIntersecting }]);
  }
}

afterEach(() => {
  vi.unstubAllGlobals();
  instances = [];
});

function setup() {
  vi.stubGlobal("IntersectionObserver", FakeIO);
  const load = vi.fn(async () => {});
  const el = {} as Element;
  const dispose = whenNear(el, load);
  return { load, dispose, io: instances[0] };
}

describe("whenNear", () => {
  it("does not load before the element is near", () => {
    const { load, io } = setup();
    io.fire(false);
    expect(load).not.toHaveBeenCalled();
  });

  it("loads exactly once across repeated intersections", () => {
    const { load, io } = setup();
    io.fire(true);
    io.fire(true);
    io.fire(true);
    expect(load).toHaveBeenCalledTimes(1);
  });

  it("does not load after dispose", () => {
    const { load, dispose, io } = setup();
    dispose();
    io.fire(true);
    expect(load).not.toHaveBeenCalled();
  });

  it("uses the default margin", () => {
    expect(setup().io.margin).toBe("100% 0px");
  });

  it("swallows a rejected load", async () => {
    vi.stubGlobal("IntersectionObserver", FakeIO);
    const load = vi.fn(async () => {
      throw new Error("boom");
    });
    whenNear({} as Element, load);
    instances[0].fire(true);
    await Promise.resolve();
    expect(load).toHaveBeenCalledTimes(1);
  });
});
