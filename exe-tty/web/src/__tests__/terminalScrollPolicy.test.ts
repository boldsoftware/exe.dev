import { describe, it, expect } from "vitest";
import {
  terminalScrollGesture,
  cursorKeyBytes,
  mouseWheelBytes,
  wheelRows,
  WheelRowAccumulator,
  DOM_DELTA_PIXEL,
  DOM_DELTA_LINE,
  DOM_DELTA_PAGE,
  type TerminalScrollModes,
} from "../lib/terminalScrollPolicy";

// A wheel tick in the terminal means different things depending on what the
// program on the other end asked for. These pin down the precedence, which
// mirrors ghostty's Surface.scrollCallback. Keep them in step.

const modes = (over: Partial<TerminalScrollModes> = {}): TerminalScrollModes => ({
  onAlternateScreen: false,
  mouseTrackingActive: false,
  alternateScrollEnabled: true,
  cursorKeysApplicationMode: false,
  ...over,
});

describe("terminalScrollGesture", () => {
  it("scrolls the local viewport for a plain shell", () => {
    // Primary screen, no mouse reporting: we own the scrollback, so a tick
    // moves our viewport and nothing goes to the PTY.
    expect(terminalScrollGesture(-3, modes())).toEqual({
      kind: "viewport",
      rows: -3,
    });
  });

  it("reports wheel presses to a mouse-tracking app", () => {
    // Claude Code (1049h + 1000h/1002h/1003h + 1006h), tmux with `mouse on`,
    // vim with `set mouse=a`, htop: the app asked to hear about the mouse, so
    // wheel presses go to it. This is the case ghostty-web@0.4.0 misses.
    expect(
      terminalScrollGesture(-2, modes({ onAlternateScreen: true, mouseTrackingActive: true })),
    ).toEqual({ kind: "mouseWheel", count: 2, up: true });

    // Mouse reporting outranks 1007 even when 1007 is off.
    expect(
      terminalScrollGesture(
        5,
        modes({
          onAlternateScreen: true,
          mouseTrackingActive: true,
          alternateScrollEnabled: false,
        }),
      ),
    ).toEqual({ kind: "mouseWheel", count: 5, up: false });
  });

  it("lets mouse reporting win on the primary screen too", () => {
    // Mouse reporting doesn't require the alt screen (e.g. a full-screen TUI
    // that never switches screens).
    expect(terminalScrollGesture(-1, modes({ mouseTrackingActive: true }))).toEqual({
      kind: "mouseWheel",
      count: 1,
      up: true,
    });
  });

  it("sends cursor keys on the alternate screen without mouse reporting", () => {
    // `less`, `man`, tmux with mouse off: DECSET 1007 (default on) asks us to
    // translate the wheel into arrow keys.
    expect(terminalScrollGesture(-4, modes({ onAlternateScreen: true }))).toEqual({
      kind: "cursorKeys",
      count: 4,
      up: true,
      applicationMode: false,
    });

    expect(
      terminalScrollGesture(2, modes({ onAlternateScreen: true, cursorKeysApplicationMode: true })),
    ).toEqual({ kind: "cursorKeys", count: 2, up: false, applicationMode: true });
  });

  it("does nothing on the alternate screen with alternate scroll disabled", () => {
    // 1007 off and no mouse reporting: the app wants neither, and the alt
    // screen has no scrollback to move.
    expect(
      terminalScrollGesture(-3, modes({ onAlternateScreen: true, alternateScrollEnabled: false })),
    ).toEqual({ kind: "none" });
  });

  it("treats zero rows as a no-op whatever the modes", () => {
    for (const onAlternateScreen of [true, false]) {
      for (const mouseTrackingActive of [true, false]) {
        expect(terminalScrollGesture(0, modes({ onAlternateScreen, mouseTrackingActive }))).toEqual(
          { kind: "none" },
        );
      }
    }
  });
});

describe("cursorKeyBytes", () => {
  it("repeats one press per row", () => {
    expect(cursorKeyBytes(3, true, false)).toBe("\x1b[A\x1b[A\x1b[A");
    expect(cursorKeyBytes(2, false, false)).toBe("\x1b[B\x1b[B");
  });

  it("honors DECCKM", () => {
    // Application cursor-key mode swaps CSI for SS3. Getting this wrong makes
    // readline-driven TUIs see literal "[A" garbage — ghostty-web hardcodes
    // CSI, which is the other half of why this needed taking over.
    expect(cursorKeyBytes(1, true, true)).toBe("\x1bOA");
    expect(cursorKeyBytes(1, false, true)).toBe("\x1bOB");
  });

  it("clamps nonsense counts", () => {
    expect(cursorKeyBytes(0, true, false)).toBe("");
    expect(cursorKeyBytes(-2, true, false)).toBe("");
  });
});

describe("mouseWheelBytes", () => {
  it("encodes SGR wheel presses (button 64 up, 65 down)", () => {
    expect(mouseWheelBytes({ count: 1, up: true, format: "sgr", col: 12, row: 7 })).toBe(
      "\x1b[<64;12;7M",
    );
    expect(mouseWheelBytes({ count: 2, up: false, format: "sgr", col: 1, row: 1 })).toBe(
      "\x1b[<65;1;1M\x1b[<65;1;1M",
    );
  });

  it("has no release report — wheel buttons don't release", () => {
    const bytes = mouseWheelBytes({
      count: 1,
      up: true,
      format: "sgr",
      col: 3,
      row: 4,
    });
    expect(bytes).not.toContain("m");
  });

  it("encodes X10 with the 32 bias and clamps past 223", () => {
    expect(mouseWheelBytes({ count: 1, up: true, format: "x10", col: 1, row: 1 })).toBe(
      "\x1b[M`!!",
    ); // 32+64='`', 32+1='!'
    const clamped = mouseWheelBytes({
      count: 1,
      up: false,
      format: "x10",
      col: 400,
      row: 500,
    });
    expect(clamped).toBe("\x1b[Ma" + String.fromCharCode(255, 255));
  });

  it("encodes urxvt and SGR-pixel variants", () => {
    expect(mouseWheelBytes({ count: 1, up: false, format: "urxvt", col: 5, row: 6 })).toBe(
      "\x1b[97;5;6M",
    );
    expect(
      mouseWheelBytes({
        count: 1,
        up: true,
        format: "sgrPixels",
        col: 5,
        row: 6,
        x: 64,
        y: 108,
      }),
    ).toBe("\x1b[<64;64;108M");
  });

  it("emits nothing for a zero count", () => {
    expect(mouseWheelBytes({ count: 0, up: true, format: "sgr", col: 1, row: 1 })).toBe("");
  });
});

describe("wheelRows", () => {
  it("divides pixel deltas by the cell height", () => {
    expect(wheelRows({ deltaY: 60, deltaMode: DOM_DELTA_PIXEL }, 20, 24)).toBe(3);
  });

  it("takes line deltas as rows and page deltas as screenfuls", () => {
    expect(wheelRows({ deltaY: -2, deltaMode: DOM_DELTA_LINE }, 20, 24)).toBe(-2);
    expect(wheelRows({ deltaY: 1, deltaMode: DOM_DELTA_PAGE }, 20, 24)).toBe(24);
  });

  it("falls back to a 20px cell when metrics are missing", () => {
    // Same fallback ghostty-web uses before its first render.
    expect(wheelRows({ deltaY: 40, deltaMode: DOM_DELTA_PIXEL }, 0, 24)).toBe(2);
  });
});

describe("WheelRowAccumulator", () => {
  const px = (deltaY: number) => ({ deltaY, deltaMode: DOM_DELTA_PIXEL });

  it("emits whole rows only, keeping the remainder", () => {
    const acc = new WheelRowAccumulator();
    // A trackpad or touch drag delivers a few pixels at a time: under one row
    // each, which is why rounding per event scrolls nothing at all.
    expect(acc.consume(px(15), 20, 24)).toBe(0); // 0.75 row
    expect(acc.consume(px(15), 20, 24)).toBe(1); // 1.5 → 1, keep 0.5
    expect(acc.consume(px(15), 20, 24)).toBe(1); // 1.25 → 1, keep 0.25
    // Four 15px events = 60px = exactly three rows, none lost to rounding.
    expect(acc.consume(px(15), 20, 24)).toBe(1);
  });

  it("drops leftover travel on a direction change", () => {
    const acc = new WheelRowAccumulator();
    expect(acc.consume(px(15), 20, 24)).toBe(0); // 0.75 row banked downward
    expect(acc.consume(px(-25), 20, 24)).toBe(-1); // not 0 rows
  });

  it("clamps one event to a screenful and banks nothing", () => {
    const acc = new WheelRowAccumulator();
    expect(acc.consume({ deltaY: 4, deltaMode: DOM_DELTA_PAGE }, 20, 10)).toBe(10);
    expect(acc.consume(px(0), 20, 10)).toBe(0);
  });

  it("resets on demand", () => {
    const acc = new WheelRowAccumulator();
    acc.consume(px(15), 20, 24);
    acc.reset();
    expect(acc.consume(px(15), 20, 24)).toBe(0);
  });

  it("ignores non-finite deltas", () => {
    const acc = new WheelRowAccumulator();
    expect(acc.consume(px(Number.NaN), 20, 24)).toBe(0);
  });
});
