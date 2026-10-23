import { describe, it, expect, vi } from "vitest";
import {
  patchGhosttyPrototype,
  installGhosttySelectionPolicy,
  installXtermSelectionPolicy,
  installGhosttyWheelPolicy,
  installGhosttySynchronizedOutput,
  GHOSTTY_SYNC_TIMEOUT_MS,
  readGhosttyScrollModes,
  terminalReportsMouse,
  type GhosttyWheelTerminal,
} from "../lib/terminalRenderer";

// Minimal stand-in for ghostty-web's Terminal prototype: open() sets
// contenteditable (which is what makes Chrome paint a second, native caret),
// and attachCustomKeyEventHandler uses inverted (ghostty) semantics.
function fakePrototype() {
  return {
    element: undefined as HTMLElement | undefined,
    handler: null as null | ((ev: KeyboardEvent) => boolean),
    open(parent: HTMLElement) {
      this.element = parent;
      parent.setAttribute("contenteditable", "true");
      return "opened";
    },
    dispose() {
      this.element?.removeAttribute("contenteditable");
      this.element = undefined;
    },
    attachCustomKeyEventHandler(cb: (ev: KeyboardEvent) => boolean) {
      this.handler = cb;
    },
  };
}

describe("patchGhosttyPrototype", () => {
  it("hides the browser caret so ghostty draws the only cursor", () => {
    const proto = fakePrototype();
    patchGhosttyPrototype(proto as never);
    const el = document.createElement("div");
    expect(proto.open(el)).toBe("opened");
    expect(el.getAttribute("contenteditable")).toBe("true");
    expect(el.style.caretColor).toBe("transparent");
  });

  it("flips key-handler semantics to xterm.js contract", () => {
    const proto = fakePrototype();
    patchGhosttyPrototype(proto as never);
    proto.attachCustomKeyEventHandler(() => true); // xterm: let terminal handle
    expect(proto.handler!(new KeyboardEvent("keydown"))).toBe(false); // ghostty: don't block
  });

  it("restores the caret on dispose so a backend toggle inherits nothing", () => {
    const proto = fakePrototype();
    patchGhosttyPrototype(proto as never);
    const el = document.createElement("div");
    proto.open(el);
    proto.dispose();
    expect(el.style.caretColor).toBe("");
    expect(el.getAttribute("contenteditable")).toBeNull();
  });

  it("removes Ghostty's capture wheel listener before reusing the host", () => {
    const proto = {
      ...fakePrototype(),
      handleWheel(ev: WheelEvent) {
        ev.preventDefault();
        ev.stopPropagation();
      },
      open(parent: HTMLElement) {
        this.element = parent;
        parent.addEventListener("wheel", this.handleWheel, { capture: true, passive: false });
      },
      dispose() {
        // ghostty-web@0.4.0-next.14 adds capture:true but removes without it.
        this.element?.removeEventListener("wheel", this.handleWheel);
        this.element = undefined;
      },
    };
    patchGhosttyPrototype(proto as never);
    const host = document.createElement("div");
    const remove = vi.spyOn(host, "removeEventListener");
    proto.open(host);
    proto.dispose();
    // happy-dom removes listeners without matching capture, unlike browsers.
    // Check the flag explicitly as well as the descendant event interaction.
    expect(remove).toHaveBeenCalledWith("wheel", proto.handleWheel, true);

    // xterm listens below this reused host. A leaked Ghostty capture listener
    // stops the wheel before it can reach xterm's screen/viewport.
    const nextScreen = document.createElement("div");
    host.appendChild(nextScreen);
    let received = 0;
    nextScreen.addEventListener("wheel", () => received++);
    const event = new WheelEvent("wheel", { bubbles: true, cancelable: true, deltaY: 40 });
    nextScreen.dispatchEvent(event);
    expect(received).toBe(1);
    expect(event.defaultPrevented).toBe(false);
  });

  it("redirects host focus to Ghostty input and removes the redirect on dispose", () => {
    const proto = fakePrototype();
    patchGhosttyPrototype(proto as never);
    const host = document.createElement("div");
    host.tabIndex = 0;
    const input = document.createElement("textarea");
    host.appendChild(input);
    document.body.appendChild(host);
    const focus = vi.spyOn(input, "focus");
    try {
      proto.open(host);
      host.focus();
      expect(document.activeElement).toBe(input);
      expect(focus).toHaveBeenCalledWith({ preventScroll: true });
      proto.dispose();
      focus.mockClear();
      host.focus();
      expect(document.activeElement).toBe(host);
      expect(focus).not.toHaveBeenCalled();
    } finally {
      host.remove();
    }
  });

  it("is idempotent", () => {
    const proto = fakePrototype();
    patchGhosttyPrototype(proto as never);
    const openAfterFirst = proto.open;
    patchGhosttyPrototype(proto as never);
    expect(proto.open).toBe(openAfterFirst);
    proto.attachCustomKeyEventHandler(() => true);
    expect(proto.handler!(new KeyboardEvent("keydown"))).toBe(false);
  });

  it("installs the wheel policy from open()", () => {
    const term = fakeGhosttyTerminal();
    const proto = Object.assign(fakePrototype(), term);
    patchGhosttyPrototype(proto as never);
    expect(proto.wheelHandler).toBeNull();
    proto.open(document.createElement("div"));
    expect(proto.wheelHandler).not.toBeNull();
  });
});

// A stand-in for an opened ghostty-web Terminal: the mode getters the policy
// reads, input() as the PTY, and a canvas whose rect gives 10x20px cells.
const CELL_W = 10;
const CELL_H = 20;

function fakeGhosttyTerminal(
  state: {
    alternate?: boolean;
    mouseTracking?: boolean;
    modes?: Record<number, boolean>;
    throwOnGetMode?: boolean;
  } = {},
) {
  const host = document.createElement("div");
  const canvas = document.createElement("canvas");
  host.appendChild(canvas);
  canvas.getBoundingClientRect = () =>
    ({
      left: 0,
      top: 0,
      width: CELL_W * 80,
      height: CELL_H * 24,
    }) as DOMRect;
  return {
    cols: 80,
    rows: 24,
    element: host,
    sent: [] as string[],
    wheelHandler: null as null | ((ev: WheelEvent) => boolean),
    attachCustomWheelEventHandler(cb?: (ev: WheelEvent) => boolean) {
      this.wheelHandler = cb ?? null;
    },
    input(data: string, wasUserInput?: boolean) {
      expect(wasUserInput).toBe(true); // anything else writes to the screen
      this.sent.push(data);
    },
    getMode(mode: number) {
      if (state.throwOnGetMode) throw new Error("terminal is not open");
      // 1007 (alternate scroll) is on by default in ghostty; everything else
      // is off until an app sets it.
      return state.modes?.[mode] ?? mode === 1007;
    },
    hasMouseTracking() {
      return state.mouseTracking ?? false;
    },
    buffer: { active: { type: state.alternate ? "alternate" : "normal" } },
  };
}

/** A wheel tick `rows` rows' worth, over cell (col 4, row 3). */
function wheel(rows: number): WheelEvent {
  const ev = new WheelEvent("wheel", {
    deltaY: rows * CELL_H,
    deltaMode: 0, // DOM_DELTA_PIXEL
  });
  // happy-dom's WheelEvent extends UIEvent, not MouseEvent, so it drops
  // clientX/clientY from the init dict. Put them back by hand.
  return Object.assign(ev, {
    clientX: 3.5 * CELL_W,
    clientY: 2.5 * CELL_H,
  });
}

describe("ghostty wheel policy", () => {
  function install(state: Parameters<typeof fakeGhosttyTerminal>[0]) {
    const term = fakeGhosttyTerminal(state);
    expect(installGhosttyWheelPolicy(term as GhosttyWheelTerminal)).toBe(true);
    return term;
  }

  it("reports the wheel to a mouse-tracking app instead of sending arrows", () => {
    // The bug this exists for: Claude Code sets 1049h + mouse tracking + SGR,
    // and ghostty-web sent it ESC[B, which walks the prompt history.
    const term = install({
      alternate: true,
      mouseTracking: true,
      modes: { 1006: true },
    });
    expect(term.wheelHandler!(wheel(3))).toBe(true);
    expect(term.sent.join("")).toBe("\x1b[<65;4;3M".repeat(3));
    expect(term.sent.join("")).not.toContain("\x1b[B");
  });

  it("reports upward ticks with button 64", () => {
    const term = install({
      alternate: true,
      mouseTracking: true,
      modes: { 1006: true },
    });
    term.wheelHandler!(wheel(-2));
    expect(term.sent.join("")).toBe("\x1b[<64;4;3M".repeat(2));
  });

  it("falls back to X10 reports when the app never asked for SGR", () => {
    const term = install({ mouseTracking: true });
    term.wheelHandler!(wheel(1));
    expect(term.sent.join("")).toBe("\x1b[Ma" + String.fromCharCode(32 + 4, 32 + 3));
  });

  it("reports the top-left cell for position-less synthetic ticks", () => {
    // The touch-drag path in Terminal.vue feeds drags back in as WheelEvents
    // with no pointer position. Wheel-scrolling apps ignore the position, but
    // it still has to be a real cell.
    const term = install({ mouseTracking: true, modes: { 1006: true } });
    term.wheelHandler!(new WheelEvent("wheel", { deltaY: CELL_H, deltaMode: 0 }));
    expect(term.sent.join("")).toBe("\x1b[<65;1;1M");
  });

  it("still sends cursor keys on the alternate screen without mouse tracking", () => {
    // `less`, `man`, tmux with mouse off. 1007 is on by default, so this is
    // ghostty's behaviour — we take it over only to add DECCKM and the
    // sub-row accumulation.
    const term = install({ alternate: true });
    expect(term.wheelHandler!(wheel(2))).toBe(true);
    expect(term.sent.join("")).toBe("\x1b[B\x1b[B");
  });

  it("sends SS3 cursor keys under DECCKM", () => {
    const term = install({ alternate: true, modes: { 1: true } });
    term.wheelHandler!(wheel(-1));
    expect(term.sent.join("")).toBe("\x1bOA");
  });

  it("swallows the tick when the app disabled alternate scroll", () => {
    const term = install({ alternate: true, modes: { 1007: false } });
    // getMode() answers false for 1007 here, which is the app having reset it.
    expect(term.wheelHandler!(wheel(3))).toBe(true);
    expect(term.sent).toEqual([]);
  });

  it("leaves the primary screen to ghostty's own smooth scroller", () => {
    const term = install({});
    expect(term.wheelHandler!(wheel(3))).toBe(false);
    expect(term.sent).toEqual([]);
  });

  it("accumulates sub-row trackpad deltas instead of rounding them away", () => {
    const term = install({
      alternate: true,
      mouseTracking: true,
      modes: { 1006: true },
    });
    // 8px of a 20px cell: less than a row, twice in a row.
    const nudge = new WheelEvent("wheel", { deltaY: 8, deltaMode: 0 });
    expect(term.wheelHandler!(nudge)).toBe(true);
    expect(term.sent).toEqual([]);
    term.wheelHandler!(nudge);
    term.wheelHandler!(nudge);
    expect(term.sent.length).toBe(1); // 24px = one whole row
  });

  it("survives mode getters that throw (closed terminal, mid-teardown)", () => {
    const term = install({ throwOnGetMode: true, alternate: true });
    expect(() => term.wheelHandler!(wheel(1))).not.toThrow();
    // Unknown 1007 defaults to on, matching ghostty's own default.
    expect(term.sent.join("")).toBe("\x1b[B");
  });

  it("installs once per terminal", () => {
    const term = fakeGhosttyTerminal();
    expect(installGhosttyWheelPolicy(term as GhosttyWheelTerminal)).toBe(true);
    const first = term.wheelHandler;
    expect(installGhosttyWheelPolicy(term as GhosttyWheelTerminal)).toBe(false);
    expect(term.wheelHandler).toBe(first);
  });

  it("declines to install on a backend without the hook", () => {
    const term = fakeGhosttyTerminal() as Partial<GhosttyWheelTerminal>;
    delete term.attachCustomWheelEventHandler;
    expect(installGhosttyWheelPolicy(term as GhosttyWheelTerminal)).toBe(false);
  });

  it("reads modes with ghostty's defaults when the backend can't answer", () => {
    const bare = { cols: 80, rows: 24 } as GhosttyWheelTerminal;
    expect(readGhosttyScrollModes(bare)).toEqual({
      onAlternateScreen: false,
      mouseTrackingActive: false,
      alternateScrollEnabled: true,
      cursorKeysApplicationMode: false,
    });
  });

  it("reads the alternate screen off wasmTerm when buffer is unbuilt", () => {
    const term = {
      cols: 80,
      rows: 24,
      wasmTerm: { isAlternateScreen: () => true },
    } as GhosttyWheelTerminal;
    expect(readGhosttyScrollModes(term).onAlternateScreen).toBe(true);
  });
});

describe("terminalReportsMouse", () => {
  it("asks ghostty-web directly", () => {
    expect(terminalReportsMouse({ hasMouseTracking: () => true } as never)).toBe(true);
    expect(terminalReportsMouse({ hasMouseTracking: () => false } as never)).toBe(false);
  });

  it("treats a ghostty terminal that is not open as not reporting", () => {
    expect(
      terminalReportsMouse({
        hasMouseTracking: () => {
          throw new Error("terminal is not open");
        },
      } as never),
    ).toBe(false);
  });

  it("reads xterm.js modes", () => {
    expect(terminalReportsMouse({ modes: { mouseTrackingMode: "none" } } as never)).toBe(false);
    expect(terminalReportsMouse({ modes: { mouseTrackingMode: "vt200" } } as never)).toBe(true);
    expect(terminalReportsMouse({} as never)).toBe(false);
  });
});

// The slice of an opened ghostty-web Terminal that the shift-selection
// override touches: the InputHandler's mouse-tracking gate and the host.
function fakeGhosttyMouseTerminal(tracking = true) {
  const host = document.createElement("div");
  const canvas = document.createElement("canvas");
  host.appendChild(canvas);
  const cleared: string[] = [];
  const selectionManager = {
    isSelecting: true,
    clearSelection: () => {
      cleared.push("clear");
    },
  };
  // Shaped like ghostty-web's InputHandler.sendMouseEvent, which encodes the
  // report and hands it to onDataCallback -- the same callback keyboard input
  // takes, and the one Terminal points at clearSelection().
  const inputHandler = {
    mouseConfig: { hasMouseTracking: () => tracking },
    onDataCallback: (_data: string) => {
      selectionManager.clearSelection();
    },
    sendMouseEvent(button: number, _col: number, _row: number, release: boolean) {
      this.onDataCallback(`${button}${release ? "m" : "M"}`);
    },
  };
  return { element: host, canvas, inputHandler, selectionManager, cleared };
}

function mouse(type: string, init: MouseEventInit = {}) {
  return new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, ...init });
}

describe("ghostty selection policy", () => {
  it("hides mouse tracking from the input handler for the length of a shift-drag", () => {
    const term = fakeGhosttyMouseTerminal();
    expect(installGhosttySelectionPolicy(term as never)).not.toBeNull();
    const gate = term.inputHandler.mouseConfig;
    expect(gate.hasMouseTracking()).toBe(true);

    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(gate.hasMouseTracking()).toBe(false);
    term.canvas.dispatchEvent(mouse("mousemove", { shiftKey: true, buttons: 1 }));
    expect(gate.hasMouseTracking()).toBe(false);
    // The release ends the gesture, but only once every listener has seen it:
    // ghostty's own mouseup handler still has to be told not to report it.
    document.dispatchEvent(mouse("mouseup", { shiftKey: true }));
    expect(gate.hasMouseTracking()).toBe(true);
  });

  it("leaves plain and right-button gestures alone", () => {
    const term = fakeGhosttyMouseTerminal();
    installGhosttySelectionPolicy(term as never);
    const gate = term.inputHandler.mouseConfig;
    term.canvas.dispatchEvent(mouse("mousedown"));
    expect(gate.hasMouseTracking()).toBe(true);
    document.dispatchEvent(mouse("mouseup"));
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true, button: 2 }));
    expect(gate.hasMouseTracking()).toBe(true);
  });

  it("recovers when the release never reaches the document", () => {
    const term = fakeGhosttyMouseTerminal();
    installGhosttySelectionPolicy(term as never);
    const gate = term.inputHandler.mouseConfig;
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(gate.hasMouseTracking()).toBe(false);
    // Released outside the window: the first motion with the button up says so.
    document.dispatchEvent(mouse("mousemove", { buttons: 0 }));
    expect(gate.hasMouseTracking()).toBe(true);

    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(gate.hasMouseTracking()).toBe(false);
    window.dispatchEvent(new Event("blur"));
    expect(gate.hasMouseTracking()).toBe(true);
  });

  it("keeps plain multi-clicks from ghostty's word selection while the app tracks the mouse", () => {
    const term = fakeGhosttyMouseTerminal();
    installGhosttySelectionPolicy(term as never);
    const reached: number[] = [];
    // Stands in for SelectionManager's canvas click listener (dbl/triple-click copy).
    term.canvas.addEventListener("click", (ev) => reached.push(ev.detail));

    term.canvas.dispatchEvent(mouse("mousedown"));
    document.dispatchEvent(mouse("mouseup"));
    term.canvas.dispatchEvent(mouse("click", { detail: 1 }));
    term.canvas.dispatchEvent(mouse("click", { detail: 2 }));
    term.canvas.dispatchEvent(mouse("click", { detail: 3 }));
    expect(reached).toEqual([1]);

    // Shift+double-click is the terminal's own word selection, as usual.
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    document.dispatchEvent(mouse("mouseup", { shiftKey: true }));
    term.canvas.dispatchEvent(mouse("click", { detail: 2, shiftKey: true }));
    expect(reached).toEqual([1, 2]);
  });

  it("does nothing when the app is not tracking the mouse", () => {
    const term = fakeGhosttyMouseTerminal(false);
    installGhosttySelectionPolicy(term as never);
    const reached: number[] = [];
    term.canvas.addEventListener("click", (ev) => reached.push(ev.detail));
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(term.inputHandler.mouseConfig.hasMouseTracking()).toBe(false);
    document.dispatchEvent(mouse("mouseup", { shiftKey: true }));
    term.canvas.dispatchEvent(mouse("mousedown"));
    document.dispatchEvent(mouse("mouseup"));
    term.canvas.dispatchEvent(mouse("click", { detail: 2 }));
    expect(reached).toEqual([2]);
  });

  it("lets only a primary press report clear the selection", () => {
    // Claude Code asks for any-event tracking, so every mouse motion is a
    // report; if each one cleared the selection, a Shift-selection would not
    // survive to the right-click that copies it.
    const term = fakeGhosttyMouseTerminal();
    installGhosttySelectionPolicy(term as never);
    const ih = term.inputHandler;
    ih.sendMouseEvent(32, 1, 1, false); // motion
    ih.sendMouseEvent(64, 1, 1, false); // wheel
    ih.sendMouseEvent(0, 1, 1, true); // release
    ih.sendMouseEvent(2, 1, 1, false); // right press
    expect(term.cleared).toEqual([]);
    expect(term.selectionManager.isSelecting).toBe(true);
    ih.sendMouseEvent(0, 1, 1, false); // left press: the app's gesture, not ghostty's
    expect(term.cleared).toEqual(["clear"]);
    expect(term.selectionManager.isSelecting).toBe(false);
    ih.onDataCallback("a"); // typing
    expect(term.cleared).toEqual(["clear", "clear"]);
  });

  it("does the same for xterm.js, whose SelectionService clears on user input", () => {
    const events: [string, boolean | undefined][] = [];
    const coreService = {
      triggerDataEvent(data: string, wasUserInput?: boolean) {
        events.push([data, wasUserInput]);
      },
    };
    expect(installXtermSelectionPolicy({ _core: { coreService } })).toBe(true);
    const report = (code: number, final = "M") => `\x1b[<${code};4;2${final}`;
    for (const data of [
      report(35),
      report(64),
      report(0, "m"),
      report(2),
      report(4),
      report(0),
      "a",
    ]) {
      coreService.triggerDataEvent(data, true);
    }
    expect(events.map(([, userInput]) => userInput)).toEqual([
      false, // motion
      false, // wheel
      false, // release
      false, // right press
      true, // Shift + primary press
      true, // primary press
      true, // typing
    ]);
    expect(installXtermSelectionPolicy({ _core: {} })).toBe(false);
  });

  it("degrades to no override on a backend without the seam", () => {
    expect(
      installGhosttySelectionPolicy({ element: document.createElement("div") } as never),
    ).toBeNull();
  });

  it("is installed by open() and torn down by dispose()", () => {
    const term = fakeGhosttyMouseTerminal();
    const proto = Object.assign(fakePrototype(), fakeGhosttyTerminal(), {
      inputHandler: term.inputHandler,
    });
    patchGhosttyPrototype(proto as never);
    proto.open(term.element);
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(term.inputHandler.mouseConfig.hasMouseTracking()).toBe(false);
    // Disposed mid-gesture: the gate is restored, not left closed.
    proto.dispose();
    expect(term.inputHandler.mouseConfig.hasMouseTracking()).toBe(true);
    term.inputHandler.sendMouseEvent(32, 1, 1, false);
    expect(term.cleared).toEqual(["clear"]);
    term.canvas.dispatchEvent(mouse("mousedown", { shiftKey: true }));
    expect(term.inputHandler.mouseConfig.hasMouseTracking()).toBe(true);
  });
});

// Secondary policy checks. terminalRenderer.browser.test.ts verifies real
// installed WASM parsing/dirty-state behavior and actual Chromium/WebKit pixels.
describe("Ghostty synchronized output lifecycle", () => {
  function setup() {
    let synchronized = false;
    const render = vi.fn();
    const renderer = { render };
    const term = {
      ...fakePrototype(),
      renderer,
      getMode: vi.fn(() => synchronized),
      write: vi.fn(),
    };
    patchGhosttyPrototype(term as never);
    term.open(document.createElement("div"));
    return { term, renderer, render, mode: (value: boolean) => (synchronized = value) };
  }

  it("never renews the hard deadline from writes or extra paint requests", () => {
    vi.useFakeTimers();
    try {
      const { term, renderer, render, mode } = setup();
      mode(true);
      term.write("first batch");
      const held = renderer.render;
      held("buffer", false);
      expect(render).not.toHaveBeenCalled();
      expect(vi.getTimerCount()).toBe(1);
      vi.advanceTimersByTime(GHOSTTY_SYNC_TIMEOUT_MS - 1);
      term.write("more output");
      held("buffer", false);
      expect(vi.getTimerCount()).toBe(1);
      vi.advanceTimersByTime(1);
      expect(render).toHaveBeenLastCalledWith("buffer", true);
      expect(vi.getTimerCount()).toBe(0);
      term.write("still held upstream");
      held("buffer", false);
      expect(vi.getTimerCount()).toBe(0);
      mode(false);
      term.write("reset");
      mode(true);
      term.write("new update");
      expect(vi.getTimerCount()).toBe(1);
      term.dispose();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a pending deadline and restores the captured renderer on dispose", () => {
    vi.useFakeTimers();
    try {
      const { term, renderer, mode } = setup();
      mode(true);
      term.write("pending output");
      expect(vi.getTimerCount()).toBe(1);
      const wrappedRender = renderer.render;
      term.dispose();
      expect(vi.getTimerCount()).toBe(0);
      expect(renderer.render).not.toBe(wrappedRender);
      vi.advanceTimersByTime(GHOSTTY_SYNC_TIMEOUT_MS * 2);
      expect(renderer.render).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("declines unsupported hooks and never double-wraps a live terminal", () => {
    expect(installGhosttySynchronizedOutput({ write() {} })).toBe(false);
    const { term } = setup();
    expect(installGhosttySynchronizedOutput(term)).toBe(false);
    term.dispose();
  });
});
