// Touch gesture handling for the web terminals.
//
// Mobile Safari treats a terminal like a text field: any touch that lands on
// the renderer's input surface focuses it, which summons the soft keyboard and
// (because iOS auto-zooms editable elements) yanks the viewport to the caret.
// That made scrolling back through output nearly impossible on an iPhone: you
// touch the screen to scroll and instead get a keyboard plus a zoom jump.
//
// Three separate things conspired to focus the terminal on every touch:
//   - The app focused the hidden input from `pointerdown` — i.e. at the *start*
//     of every touch, before you can tell a tap from a scroll.
//   - ghostty-web focuses its own hidden <textarea> from an unconditional
//     `touchend` listener on its canvas.
//   - Browsers replay a touch as mousedown/mouseup/click, and both backends
//     focus from mousedown. Suppressing only the touch events isn't enough.
//
// The backends also differ in whether a drag scrolls anything at all:
// ghostty-web paints a canvas and handles only `wheel`, so a drag through its
// output did nothing but raise the keyboard; xterm.js 6 scrolls its virtual
// viewport from a document-level Gesture helper of its own.
//
// This module owns the gesture instead:
//   - A short, stationary touch is a tap: focus the terminal. This is the only
//     path that may summon the keyboard.
//   - A touch that moves is a scroll: never focus, and swallow both the
//     touchend and the mouse replay so neither backend can refocus. Where the
//     backend won't scroll on touch (ghostty), feed the drag back as wheel
//     events; where it will (xterm.js), stay out of the way rather than
//     double-scroll.
//   - A long stationary press keeps its default action so iOS still offers its
//     "Paste" menu, but does not focus (a keyboard would cover the menu).
//   - Multi-touch is left to the browser so pinch-zoom keeps working, and stays
//     latched until every finger lifts so no intermediate touchend focuses.

/** Movement (px) beyond which a touch is a drag, not a tap. */
export const TAP_SLOP_PX = 10;
/** Maximum duration (ms) of a tap; longer stationary presses are long-presses. */
export const TAP_MAX_MS = 500;

/**
 * Pixels of finger travel per "line" of scroll, and the most lines we put in
 * one scrollBy() call.
 *
 * Consumers of a pixel-mode wheel delta routinely quantize it to whole lines:
 * ghostty-web, on the alternate screen (full-screen TUIs), emits
 * `round(deltaY / 33)` arrow keys and caps that at 5 per event. A 60Hz drag
 * delivers only ~5-15px per touchmove, so forwarding each event's delta
 * verbatim rounds every one of them to zero and the drag barely moves. Batching
 * finger travel into 33px units (the de-facto "one wheel line", and the divisor
 * ghostty uses) makes a drag scroll the distance the finger actually moved.
 *
 * The wheel policy in terminalRenderer.ts now carries the sub-row remainder
 * itself, so quantization no longer eats slow drags on the paths it owns
 * (mouse-reporting apps, and alternate-scroll cursor keys). Batching stays for
 * the path it doesn't: ghostty-web's own viewport scroller, which still rounds
 * per event.
 */
export const WHEEL_LINE_PX = 33;
export const MAX_WHEEL_LINES = 5;

/**
 * How long after a non-tap touch we keep swallowing synthesized mouse events.
 *
 * Browsers replay a touch as mousedown/mouseup/click a moment after touchend
 * (for pages written before touch existed). Both backends focus from mousedown
 * — ghostty-web from both its canvas and its selection manager — so a
 * long-press or the end of a pinch would focus via that replay even though we
 * suppressed the touch events themselves. 700ms comfortably covers the replay
 * without swallowing a subsequent real click.
 */
const SYNTHETIC_MOUSE_MS = 700;

export interface TerminalTouchOptions {
  /** Focus the terminal's real input element (the only keyboard-summoning path). */
  focus(): void;
  /**
   * True when this module must scroll the terminal itself because the live
   * backend does nothing on touch.
   *
   * This is a per-backend capability, not something we can read off the DOM:
   * xterm.js 6 scrolls its *virtual* viewport (scrollHeight === clientHeight,
   * so it looks unscrollable) from a document-level touch Gesture of its own,
   * while ghostty-web paints a canvas and handles only `wheel`. Getting this
   * wrong in the "we scroll" direction double-scrolls (our rows plus the
   * backend's); getting it wrong the other way scrolls not at all. The e2e
   * spec measures rows-per-drag on both backends so an upstream change that
   * flips this fails loudly instead of silently double-scrolling.
   */
  ownsScrolling(): boolean;
  /**
   * Scroll the terminal by `deltaY` CSS pixels of finger travel; positive
   * scrolls toward newer output (same sign convention as WheelEvent.deltaY).
   */
  scrollBy(deltaY: number): void;
}

export function attachTerminalTouch(host: HTMLElement, opts: TerminalTouchOptions): () => void {
  // What we've decided this gesture is. 'multi' latches until every finger
  // lifts: a pinch delivers one touchend per finger, and each of them would
  // otherwise reach the renderer's focus-on-touchend.
  let phase: "idle" | "pending" | "drag" | "multi" = "idle";
  let startX = 0;
  let startY = 0;
  let startAt = 0;
  let lastY = 0;
  // Unflushed finger travel. Wheel deltas below one line are rounded to zero by
  // consumers (see WHEEL_LINE_PX), so batch instead of dropping the remainder.
  let pending = 0;
  // Wall clock through which to swallow the browser's synthesized mouse events.
  // 0 means "let them through" (a tap, or genuine mouse input).
  let suppressMouseUntil = 0;

  const onStart = (ev: Event) => {
    const live = liveTouches(ev);
    if (live.length !== 1 || phase === "multi") {
      // Second finger down: pinch/two-finger gesture. Hands off until all lift,
      // and keep it away from the backend's gesture recognizer — see onMove.
      phase = "multi";
      stop(ev);
      return;
    }
    phase = "pending";
    pending = 0;
    startX = live[0].clientX;
    startY = live[0].clientY;
    lastY = startY;
    startAt = now();
  };

  const onMove = (ev: Event) => {
    if (phase === "idle") return;
    const live = liveTouches(ev);
    if (phase === "multi" || live.length !== 1) {
      phase = "multi";
      // Keep multi-touch away from the backend's own gesture recognizer, which
      // would otherwise interpret a pinch as a scroll (xterm.js dispatches its
      // Gesture from these very events). We never preventDefault here: a pinch
      // is the browser's to handle.
      stop(ev);
      return;
    }
    const { clientX: x, clientY: y } = live[0];
    if (Math.abs(x - startX) > TAP_SLOP_PX || Math.abs(y - startY) > TAP_SLOP_PX) phase = "drag";
    const dy = lastY - y;
    lastY = y;
    if (!opts.ownsScrolling()) {
      // The backend handles touch scrolling itself; stay out of the way so a
      // drag doesn't scroll twice as far as the finger moved.
      return;
    }
    // The backend won't scroll on touch, so drive it ourselves. Dragging the
    // finger up moves the content up, i.e. toward newer output (positive
    // delta, matching WheelEvent.deltaY).
    ev.preventDefault();
    pending += dy;
    // Flush whole lines only, carrying the remainder into the next move. A
    // 60Hz drag delivers ~5-15px per event, and a consumer that quantizes to
    // whole lines rounds each of those to zero — so emitting per-event deltas
    // loses almost the entire drag. See WHEEL_LINE_PX.
    while (Math.abs(pending) >= WHEEL_LINE_PX) {
      const lines = clampMagnitude(Math.trunc(pending / WHEEL_LINE_PX), MAX_WHEEL_LINES);
      const delta = lines * WHEEL_LINE_PX;
      pending -= delta;
      opts.scrollBy(delta);
    }
  };

  const onEnd = (ev: Event) => {
    const remaining = liveTouches(ev).length;
    const ended = phase;
    // A pinch lifts fingers one at a time; stay latched until the last one is
    // up so no intermediate touchend reaches the renderer.
    phase = remaining > 0 ? "multi" : "idle";
    pending = 0;

    if (ended === "idle") return;
    if (ended === "multi") {
      // Pinch-zoom. Suppress the renderer's focus-on-touchend (it would raise
      // the keyboard on top of the zoom) but leave the default action alone so
      // the browser's own zoom handling is untouched.
      stop(ev);
      suppressMouseUntil = now() + SYNTHETIC_MOUSE_MS;
      return;
    }
    if (ended === "drag") {
      // End of a scroll. Swallow it so the renderer's focus-on-touchend (and
      // the synthesized mousedown that would also focus) never runs: scrolling
      // must not summon the keyboard.
      ev.preventDefault();
      stop(ev);
      suppressMouseUntil = now() + SYNTHETIC_MOUSE_MS;
      return;
    }
    if (now() - startAt > TAP_MAX_MS) {
      // Long press. Don't focus (that's a keyboard), and don't preventDefault —
      // iOS's own text-interaction menu ("Paste") is the point of a long press,
      // and it only appears if the default action runs. Stop propagation so the
      // renderer doesn't focus behind the menu, and swallow the mouse replay
      // that would otherwise focus anyway.
      stop(ev);
      suppressMouseUntil = now() + SYNTHETIC_MOUSE_MS;
      return;
    }
    // A real tap. We own focus so it lands on one predictable element; stop the
    // event from reaching the renderer's competing touch handler, but don't
    // preventDefault — the synthesized click still needs to reach link
    // handling, and the mouse replay is welcome (it sets the caret/selection
    // where the user tapped).
    suppressMouseUntil = 0;
    stop(ev);
    opts.focus();
  };

  // Swallow the mouse events the browser replays after a gesture we classified
  // as "must not focus". Only fires in the window right after such a gesture,
  // so real mouse input on a touch-capable laptop is unaffected.
  const onMouse = (ev: Event) => {
    if (!suppressMouseUntil) return;
    if (now() > suppressMouseUntil) {
      suppressMouseUntil = 0;
      return;
    }
    ev.preventDefault();
    stop(ev);
  };

  const onCancel = (ev: Event) => {
    phase = liveTouches(ev).length > 0 ? "multi" : "idle";
    pending = 0;
    suppressMouseUntil = now() + SYNTHETIC_MOUSE_MS;
  };

  // Capture phase: the renderer may bind its own touch handlers to this element
  // or its descendants (ghostty-web listens on its canvas), so we must see the
  // event first to be able to suppress them. Note this cannot suppress
  // xterm.js's scroll gesture, which listens on `document` — that's deliberate,
  // since xterm.js owns its own touch scrolling.
  const capture = { capture: true, passive: false } as const;
  host.addEventListener("touchstart", onStart, capture);
  host.addEventListener("touchmove", onMove, capture);
  host.addEventListener("touchend", onEnd, capture);
  host.addEventListener("touchcancel", onCancel, capture);
  for (const type of MOUSE_REPLAY_EVENTS) host.addEventListener(type, onMouse, capture);
  return () => {
    host.removeEventListener("touchstart", onStart, capture);
    host.removeEventListener("touchmove", onMove, capture);
    host.removeEventListener("touchend", onEnd, capture);
    host.removeEventListener("touchcancel", onCancel, capture);
    for (const type of MOUSE_REPLAY_EVENTS) host.removeEventListener(type, onMouse, capture);
  };
}

// The mouse events a browser replays after a touch. mousedown is the one that
// focuses in both backends; the others are suppressed too so a half-handled
// sequence can't leave either backend mid-selection.
const MOUSE_REPLAY_EVENTS = ["mousedown", "mouseup", "click"] as const;

function clampMagnitude(n: number, max: number): number {
  return Math.sign(n) * Math.min(Math.abs(n), max);
}

function stop(ev: Event) {
  ev.stopPropagation();
  // Also needed when the renderer listens on the same element we do.
  ev.stopImmediatePropagation();
}

// Fingers still on the glass. Deliberately NOT falling back to
// changedTouches: on the first touchend of a two-finger gesture `touches` holds
// the one remaining finger, and treating that as a fresh single-finger gesture
// is how a pinch gets misread as a drag.
function liveTouches(ev: Event): { clientX: number; clientY: number }[] {
  const te = ev as TouchEvent;
  return te.touches ? Array.from(te.touches) : [];
}

function now(): number {
  return typeof performance !== "undefined" && typeof performance.now === "function"
    ? performance.now()
    : Date.now();
}
