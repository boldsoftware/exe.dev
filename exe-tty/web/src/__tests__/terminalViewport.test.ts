import { describe, it, expect } from "vitest";
import { computeViewportClamp, KEYBOARD_MIN_PX } from "../lib/terminalViewport";

describe("computeViewportClamp", () => {
  it("leaves the layout alone with no keyboard", () => {
    expect(computeViewportClamp({ height: 844, scale: 1, innerHeight: 844 })).toEqual({
      keyboardUp: false,
      clampPx: null,
    });
  });

  it("clamps to the visible height when the keyboard is up", () => {
    expect(computeViewportClamp({ height: 500, scale: 1, innerHeight: 844 })).toEqual({
      keyboardUp: true,
      clampPx: 500,
    });
  });

  it("ignores address-bar-sized changes", () => {
    const h = 844 - (KEYBOARD_MIN_PX - 20);
    expect(computeViewportClamp({ height: h, scale: 1, innerHeight: 844 }).keyboardUp).toBe(false);
  });

  it("does not mistake pinch-zoom for a keyboard", () => {
    // Zoomed 2x with no keyboard: visualViewport.height halves, but the layout
    // viewport is unchanged.
    expect(computeViewportClamp({ height: 422, scale: 2, innerHeight: 844 })).toEqual({
      keyboardUp: false,
      clampPx: null,
    });
  });

  it("still detects the keyboard while zoomed", () => {
    // Zoomed 2x with a 344px keyboard: 500 layout px remain visible.
    expect(computeViewportClamp({ height: 250, scale: 2, innerHeight: 844 })).toEqual({
      keyboardUp: true,
      clampPx: 500,
    });
  });

  it("treats a bogus scale as 1", () => {
    expect(computeViewportClamp({ height: 500, scale: 0, innerHeight: 844 })).toEqual({
      keyboardUp: true,
      clampPx: 500,
    });
  });
});
