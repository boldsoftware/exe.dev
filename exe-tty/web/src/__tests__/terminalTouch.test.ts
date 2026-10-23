import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  attachTerminalTouch,
  TAP_SLOP_PX,
  TAP_MAX_MS,
  WHEEL_LINE_PX,
  MAX_WHEEL_LINES,
} from "../lib/terminalTouch";

// happy-dom has no Touch/TouchEvent constructors, and we only read
// `touches`/`changedTouches`, so synthesize the shape our handlers use.
type Pt = { clientX: number; clientY: number };

// `pts` are the fingers still on the glass (event.touches); `changed` are the
// ones this event is about (event.changedTouches), defaulting to `pts` for
// start/move. A touchend passes the remaining fingers as `pts` and the lifted
// one as `changed`, which is what the browser does.
function fire(el: HTMLElement, type: string, pts: Pt[], changed?: Pt[]): Event {
  const ev = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "touches", { value: type === "touchend" ? (pts ?? []) : pts });
  Object.defineProperty(ev, "changedTouches", { value: changed ?? pts });
  el.dispatchEvent(ev);
  return ev;
}

function harness(opts: { ownsScrolling?: boolean } = {}) {
  const host = document.createElement("div");
  document.body.appendChild(host);
  const focus = vi.fn();
  const scrollBy = vi.fn();
  const dispose = attachTerminalTouch(host, {
    focus,
    ownsScrolling: () => opts.ownsScrolling ?? false,
    scrollBy,
  });
  return { host, focus, scrollBy, dispose };
}

describe("attachTerminalTouch", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    vi.useRealTimers();
  });

  it("focuses the terminal on a quick, stationary tap", () => {
    const { host, focus } = harness();
    fire(host, "touchstart", [{ clientX: 50, clientY: 50 }]);
    fire(host, "touchend", [], [{ clientX: 51, clientY: 52 }]);
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it("does not focus (or summon the keyboard) after a scroll drag", () => {
    const { host, focus } = harness();
    fire(host, "touchstart", [{ clientX: 50, clientY: 300 }]);
    fire(host, "touchmove", [{ clientX: 50, clientY: 300 - TAP_SLOP_PX - 5 }]);
    fire(host, "touchend", [], [{ clientX: 50, clientY: 300 - TAP_SLOP_PX - 5 }]);
    expect(focus).not.toHaveBeenCalled();
  });

  it("swallows the drag-ending touchend so the renderer cannot refocus", () => {
    const { host } = harness();
    const seen: string[] = [];
    // Stand-in for ghostty-web's canvas touchend handler, which focuses the
    // terminal on *every* touchend — including the end of a scroll drag.
    host.addEventListener("touchend", () => seen.push("renderer"));
    fire(host, "touchstart", [{ clientX: 10, clientY: 200 }]);
    fire(host, "touchmove", [{ clientX: 10, clientY: 100 }]);
    const end = fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
    expect(seen).toEqual([]);
    expect(end.defaultPrevented).toBe(true);
  });

  it("lets a tap through to the renderer without double-handling", () => {
    const { host, focus } = harness();
    const seen: string[] = [];
    host.addEventListener("touchend", () => seen.push("renderer"));
    fire(host, "touchstart", [{ clientX: 10, clientY: 200 }]);
    fire(host, "touchend", [], [{ clientX: 10, clientY: 200 }]);
    // We own focus for taps; the renderer's competing handler is suppressed
    // so focus lands on one predictable element.
    expect(focus).toHaveBeenCalledTimes(1);
    expect(seen).toEqual([]);
  });

  it("scrolls in whole wheel lines toward newer output when it owns scrolling", () => {
    const { host, scrollBy } = harness({ ownsScrolling: true });
    fire(host, "touchstart", [{ clientX: 10, clientY: 300 }]);
    // Finger up by one line + a bit: one line flushes, the rest is carried.
    const mv = fire(host, "touchmove", [{ clientX: 10, clientY: 300 - WHEEL_LINE_PX - 5 }]);
    expect(scrollBy).toHaveBeenCalledExactlyOnceWith(WHEEL_LINE_PX);
    expect(mv.defaultPrevented).toBe(true);
  });

  it("scrolls back into history when the finger moves down", () => {
    const { host, scrollBy } = harness({ ownsScrolling: true });
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchmove", [{ clientX: 10, clientY: 100 + 2 * WHEEL_LINE_PX }]);
    expect(scrollBy).toHaveBeenCalledExactlyOnceWith(-2 * WHEEL_LINE_PX);
  });

  it("accumulates sub-line movement instead of dropping it", () => {
    // The bug this guards: a 60Hz drag delivers ~5-15px per touchmove, and
    // consumers quantize a pixel wheel delta to whole lines — so forwarding
    // each event verbatim rounds every one to zero and the drag barely moves.
    const { host, scrollBy } = harness({ ownsScrolling: true });
    const STEP = 5;
    const steps = 24; // 120px of travel
    fire(host, "touchstart", [{ clientX: 10, clientY: 500 }]);
    for (let i = 1; i <= steps; i++) {
      fire(host, "touchmove", [{ clientX: 10, clientY: 500 - i * STEP }]);
    }
    const scrolled = scrollBy.mock.calls.reduce((sum, [d]) => sum + (d as number), 0);
    const travel = steps * STEP;
    // Everything but the sub-line remainder must have been delivered.
    expect(scrolled).toBeGreaterThan(travel - WHEEL_LINE_PX);
    expect(scrolled).toBeLessThanOrEqual(travel);
    // And in whole lines, never a partial one.
    for (const [d] of scrollBy.mock.calls) expect((d as number) % WHEEL_LINE_PX).toBe(0);
  });

  it("caps a single scroll call so a fast fling cannot overshoot wildly", () => {
    const { host, scrollBy } = harness({ ownsScrolling: true });
    fire(host, "touchstart", [{ clientX: 10, clientY: 2000 }]);
    // One enormous jump (a fling, or a coalesced move) worth 30 lines.
    fire(host, "touchmove", [{ clientX: 10, clientY: 2000 - 30 * WHEEL_LINE_PX }]);
    for (const [d] of scrollBy.mock.calls) {
      expect(Math.abs(d as number)).toBeLessThanOrEqual(MAX_WHEEL_LINES * WHEEL_LINE_PX);
    }
    // Still delivers the full travel, just split across calls.
    const scrolled = scrollBy.mock.calls.reduce((sum, [d]) => sum + (d as number), 0);
    expect(scrolled).toBe(30 * WHEEL_LINE_PX);
  });

  it("does not scroll at all when the backend handles touch itself", () => {
    // Double-scrolling (our lines plus the backend's) is the failure mode here:
    // xterm.js 6 has its own touch Gesture, so we must not also scroll.
    const { host, scrollBy } = harness({ ownsScrolling: false });
    fire(host, "touchstart", [{ clientX: 10, clientY: 300 }]);
    const mv = fire(host, "touchmove", [{ clientX: 10, clientY: 200 }]);
    expect(scrollBy).not.toHaveBeenCalled();
    expect(mv.defaultPrevented).toBe(false);
  });

  it("ignores multi-touch gestures so pinch-zoom still works", () => {
    const { host, focus, scrollBy } = harness({ ownsScrolling: true });
    fire(host, "touchstart", [
      { clientX: 10, clientY: 100 },
      { clientX: 80, clientY: 300 },
    ]);
    const mv = fire(host, "touchmove", [
      { clientX: 10, clientY: 140 },
      { clientX: 80, clientY: 260 },
    ]);
    fire(host, "touchend", [{ clientX: 10, clientY: 140 }], [{ clientX: 80, clientY: 260 }]);
    expect(focus).not.toHaveBeenCalled();
    expect(scrollBy).not.toHaveBeenCalled();
    // Never preventDefault a pinch: that IS the browser's zoom.
    expect(mv.defaultPrevented).toBe(false);
  });

  it("hides multi-touch from the backend gesture recognizer", () => {
    // xterm.js dispatches its own scroll Gesture from these events, so a pinch
    // reaching it would be read as a scroll. And we must never preventDefault a
    // pinch, since that is the browser's zoom.
    const { host } = harness({ ownsScrolling: false });
    const seen: string[] = [];
    for (const type of ["touchstart", "touchmove", "touchend"]) {
      host.addEventListener(type, () => seen.push(type));
    }
    fire(host, "touchstart", [
      { clientX: 10, clientY: 100 },
      { clientX: 80, clientY: 300 },
    ]);
    const mv = fire(host, "touchmove", [
      { clientX: 10, clientY: 60 },
      { clientX: 80, clientY: 340 },
    ]);
    fire(host, "touchend", [{ clientX: 80, clientY: 340 }], [{ clientX: 10, clientY: 60 }]);
    fire(host, "touchend", [], [{ clientX: 80, clientY: 340 }]);
    expect(seen).toEqual([]);
    expect(mv.defaultPrevented).toBe(false);
  });

  it("still lets single-finger moves reach a backend that scrolls itself", () => {
    // The flip side: xterm.js scrolls from those very touchmoves, so a
    // single-finger drag must NOT be stopped or scrolling breaks entirely.
    const { host } = harness({ ownsScrolling: false });
    const seen: string[] = [];
    host.addEventListener("touchmove", () => seen.push("touchmove"));
    fire(host, "touchstart", [{ clientX: 10, clientY: 300 }]);
    fire(host, "touchmove", [{ clientX: 10, clientY: 250 }]);
    fire(host, "touchmove", [{ clientX: 10, clientY: 200 }]);
    expect(seen).toEqual(["touchmove", "touchmove"]);
  });

  it("stays hands-off for every finger lifting out of a pinch", () => {
    // A pinch delivers one touchend per finger. The first lift leaves one
    // finger down, which must not be read as a fresh single-finger gesture —
    // otherwise the second lift focuses the terminal and the keyboard slides
    // up over the user's zoom.
    const { host, focus } = harness({ ownsScrolling: true });
    const seen: string[] = [];
    host.addEventListener("touchend", () => seen.push("renderer"));
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchstart", [
      { clientX: 10, clientY: 100 },
      { clientX: 80, clientY: 300 },
    ]);
    fire(host, "touchmove", [
      { clientX: 10, clientY: 90 },
      { clientX: 80, clientY: 310 },
    ]);
    // First finger up: one still down.
    fire(host, "touchend", [{ clientX: 80, clientY: 310 }], [{ clientX: 10, clientY: 90 }]);
    // Second finger up: none left.
    fire(host, "touchend", [], [{ clientX: 80, clientY: 310 }]);
    expect(focus).not.toHaveBeenCalled();
    expect(seen).toEqual([]);
  });

  it("handles a fresh gesture after a pinch", () => {
    const { host, focus } = harness({ ownsScrolling: true });
    fire(host, "touchstart", [
      { clientX: 10, clientY: 100 },
      { clientX: 80, clientY: 300 },
    ]);
    fire(host, "touchend", [{ clientX: 80, clientY: 300 }], [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchend", [], [{ clientX: 80, clientY: 300 }]);
    expect(focus).not.toHaveBeenCalled();
    // A plain tap now works normally again.
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it("lets iOS offer Paste on a long press without focusing the terminal", () => {
    vi.useFakeTimers();
    const { host, focus } = harness();
    const seen: string[] = [];
    // Stand-in for ghostty-web's canvas touchend handler, which focuses
    // unconditionally — it must not run, or the keyboard covers the very Paste
    // menu the long press was for.
    host.addEventListener("touchend", () => seen.push("renderer"));
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    vi.advanceTimersByTime(TAP_MAX_MS + 50);
    const end = fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
    expect(focus).not.toHaveBeenCalled();
    expect(seen).toEqual([]);
    // The default action must survive: it's what shows the text-interaction menu.
    expect(end.defaultPrevented).toBe(false);
  });

  it("resets state on touchcancel", () => {
    const { host, focus } = harness();
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchcancel", [], [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
    expect(focus).not.toHaveBeenCalled();
  });

  it("stops handling after dispose", () => {
    const { host, focus, dispose } = harness();
    dispose();
    fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
    fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
    expect(focus).not.toHaveBeenCalled();
  });

  // Browsers replay a touch as mousedown/mouseup/click shortly after touchend.
  // Both backends focus from mousedown, so a gesture we deliberately didn't
  // focus on would focus anyway via the replay — and on iOS that means the
  // keyboard slides up over the Paste menu / over the user's pinch-zoom.
  describe("synthesized mouse replay", () => {
    const replay = (host: HTMLElement) => {
      const seen: string[] = [];
      for (const type of ["mousedown", "mouseup", "click"]) {
        host.addEventListener(type, () => seen.push(type));
        const ev = new Event(type, { bubbles: true, cancelable: true });
        host.dispatchEvent(ev);
      }
      return seen;
    };

    it("is swallowed after a long press", () => {
      vi.useFakeTimers();
      const { host } = harness();
      fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
      vi.advanceTimersByTime(TAP_MAX_MS + 50);
      fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
      expect(replay(host)).toEqual([]);
    });

    it("is swallowed after a scroll drag", () => {
      const { host } = harness();
      fire(host, "touchstart", [{ clientX: 10, clientY: 300 }]);
      fire(host, "touchmove", [{ clientX: 10, clientY: 150 }]);
      fire(host, "touchend", [], [{ clientX: 10, clientY: 150 }]);
      expect(replay(host)).toEqual([]);
    });

    it("is swallowed after a pinch", () => {
      const { host } = harness();
      fire(host, "touchstart", [
        { clientX: 10, clientY: 100 },
        { clientX: 80, clientY: 300 },
      ]);
      fire(host, "touchend", [{ clientX: 80, clientY: 300 }], [{ clientX: 10, clientY: 100 }]);
      fire(host, "touchend", [], [{ clientX: 80, clientY: 300 }]);
      expect(replay(host)).toEqual([]);
    });

    it("is allowed through after a tap, so the caret lands where the user tapped", () => {
      const { host } = harness();
      fire(host, "touchstart", [{ clientX: 10, clientY: 100 }]);
      fire(host, "touchend", [], [{ clientX: 10, clientY: 100 }]);
      expect(replay(host)).toEqual(["mousedown", "mouseup", "click"]);
    });

    it("does not swallow genuine mouse input (no preceding touch)", () => {
      const { host } = harness();
      expect(replay(host)).toEqual(["mousedown", "mouseup", "click"]);
    });

    it("stops swallowing once the replay window has passed", () => {
      vi.useFakeTimers();
      const { host } = harness();
      fire(host, "touchstart", [{ clientX: 10, clientY: 300 }]);
      fire(host, "touchmove", [{ clientX: 10, clientY: 150 }]);
      fire(host, "touchend", [], [{ clientX: 10, clientY: 150 }]);
      vi.advanceTimersByTime(2000);
      expect(replay(host)).toEqual(["mousedown", "mouseup", "click"]);
    });
  });
});
