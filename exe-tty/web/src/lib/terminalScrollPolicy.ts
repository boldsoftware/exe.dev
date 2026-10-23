// What a vertical scroll gesture over the terminal should actually do.
//
// A wheel tick is not unconditionally "move my viewport". The program on the
// far end of the PTY may have asked to receive mouse events, or asked us to
// translate the wheel into arrow keys, and the precedence between those is not
// something the browser knows.
//
// ghostty-web@0.4.0 gets that precedence wrong: its handleWheel() emits
// ESC[A/ESC[B whenever the alternate screen is up, without checking whether the
// application enabled mouse reporting (DEC 9/1000/1002/1003). Claude Code sets
// 1049h + 1000h/1002h/1003h + 1006h at startup — it scrolls its own message
// list from SGR wheel reports — so on ghostty-web the wheel instead walks the
// prompt's history and Claude warns "Scroll wheel is sending arrow keys". This
// module is the missing decision, applied through the terminal's public
// customWheelEventHandler hook (see terminalRenderer.ts).
//
// It is a deliberate port of ghostty's own Surface.scrollCallback. Keep them
// in step: same precedence, same names, same defaults.
// terminalScrollPolicy.test.ts asserts the table.

/** The four things a vertical scroll gesture can mean. */
export type TerminalScrollGesture =
  /** Scroll our own scrollback viewport by `rows` (negative = into history). */
  | { kind: "viewport"; rows: number }
  /** Report `count` wheel button presses to the application. */
  | { kind: "mouseWheel"; count: number; up: boolean }
  /** Send `count` cursor-key presses (DECSET 1007, "alternate scroll"). */
  | { kind: "cursorKeys"; count: number; up: boolean; applicationMode: boolean }
  /** Do nothing. */
  | { kind: "none" };

/** The terminal state the decision depends on. */
export interface TerminalScrollModes {
  /** DEC mode 1047/1049 active. */
  onAlternateScreen: boolean;
  /** Any of DEC 9/1000/1002/1003. */
  mouseTrackingActive: boolean;
  /** DEC mode 1007. Defaults to ON in ghostty (see terminal/modes.zig). */
  alternateScrollEnabled: boolean;
  /** DECCKM (DEC mode 1). */
  cursorKeysApplicationMode: boolean;
}

/** Decides what a scroll gesture of `rows` (negative = up/into history) means. */
export function terminalScrollGesture(
  rows: number,
  modes: TerminalScrollModes,
): TerminalScrollGesture {
  if (rows === 0) return { kind: "none" };
  const up = rows < 0;
  const count = Math.abs(rows);

  // 1. The application asked to hear about the mouse. It wins on both screens:
  //    it may be drawing its own scrollback (tmux copy-mode, claude's message
  //    list) that we know nothing about.
  if (modes.mouseTrackingActive) return { kind: "mouseWheel", count, up };

  // 2. Alternate screen, no mouse reporting. Our scrollback is not the user's
  //    scrollback here, so translate to arrow keys when the app allows it
  //    (1007, on by default) and otherwise swallow.
  if (modes.onAlternateScreen) {
    if (!modes.alternateScrollEnabled) return { kind: "none" };
    return {
      kind: "cursorKeys",
      count,
      up,
      applicationMode: modes.cursorKeysApplicationMode,
    };
  }

  // 3. Plain primary screen: we own the scrollback.
  return { kind: "viewport", rows };
}

/** Bytes for `count` cursor-key presses. DECCKM switches CSI to SS3. */
export function cursorKeyBytes(count: number, up: boolean, applicationMode: boolean): string {
  if (count <= 0) return "";
  const seq = applicationMode ? (up ? "\x1bOA" : "\x1bOB") : up ? "\x1b[A" : "\x1b[B";
  return seq.repeat(count);
}

/**
 * How the application wants mouse reports encoded.
 *
 * "x10" is the base protocol every app gets by default; the others are opted
 * into with DEC 1006 (SGR), 1015 (urxvt) and 1016 (SGR-pixels).
 */
export type MouseReportFormat = "x10" | "sgr" | "sgrPixels" | "urxvt";

export interface MouseWheelReport {
  count: number;
  up: boolean;
  format: MouseReportFormat;
  /** 1-based cell under the pointer. */
  col: number;
  row: number;
  /** 1-based pixel offset inside the terminal area; "sgrPixels" only. */
  x?: number;
  y?: number;
}

/**
 * Bytes for `count` wheel reports.
 *
 * Wheel buttons are 64 (up) and 65 (down), and — unlike real buttons — they
 * have no release event, so there is nothing to send on the way back up.
 */
export function mouseWheelBytes(report: MouseWheelReport): string {
  if (report.count <= 0) return "";
  const button = report.up ? 64 : 65;
  const col = Math.max(1, Math.floor(report.col));
  const row = Math.max(1, Math.floor(report.row));
  let one: string;
  switch (report.format) {
    case "sgr":
      one = `\x1b[<${button};${col};${row}M`;
      break;
    case "sgrPixels": {
      const x = Math.max(1, Math.round(report.x ?? col));
      const y = Math.max(1, Math.round(report.y ?? row));
      one = `\x1b[<${button};${x};${y}M`;
      break;
    }
    case "urxvt":
      one = `\x1b[${button + 32};${col};${row}M`;
      break;
    case "x10": {
      // X10 packs button and position into single bytes biased by 32, so
      // anything past 223 is unreportable. Clamp rather than emit bytes that
      // would decode as a wildly different cell.
      one =
        "\x1b[M" +
        String.fromCharCode(32 + button, 32 + Math.min(col, 223), 32 + Math.min(row, 223));
      break;
    }
  }
  return one.repeat(report.count);
}

/** WheelEvent.deltaMode values, spelled out (happy-dom has no WheelEvent constants). */
export const DOM_DELTA_PIXEL = 0;
export const DOM_DELTA_LINE = 1;
export const DOM_DELTA_PAGE = 2;

/** The part of a WheelEvent that decides how far to scroll. */
export interface WheelDelta {
  deltaY: number;
  deltaMode: number;
}

/**
 * Turns wheel deltas into whole rows, keeping the sub-row remainder.
 *
 * Mouse reports and cursor keys are quantized to rows, and a trackpad or a
 * touch drag delivers only a few pixels per event — well under one row. Round
 * each event on its own (as ghostty-web does) and every one of them rounds to
 * zero, which is the "nothing scrolls at all" half of this bug. Carrying the
 * remainder makes a slow drag cover the distance the finger actually moved.
 *
 * The iOS app's TerminalScrollAccumulator does the same job for pan gestures.
 */
export class WheelRowAccumulator {
  private residual = 0;

  reset() {
    this.residual = 0;
  }

  /**
   * Whole rows available from `ev`.
   *
   * `cellHeight` is CSS pixels per row; `viewportRows` bounds a single event to
   * one screenful so a DOM_DELTA_PAGE tick (or a synthetic monster delta) can't
   * fire hundreds of arrow keys.
   */
  consume(ev: WheelDelta, cellHeight: number, viewportRows: number): number {
    const rowsFloat = wheelRows(ev, cellHeight, viewportRows);
    if (rowsFloat === 0 && this.residual === 0) return 0;
    // A direction change makes leftover travel from the other direction
    // meaningless — it would eat the first rows of the new gesture.
    if (rowsFloat !== 0 && Math.sign(rowsFloat) !== Math.sign(this.residual)) {
      this.residual = 0;
    }
    const total = this.residual + rowsFloat;
    const whole = Math.trunc(total);
    this.residual = total - whole;
    const limit = Math.max(1, Math.trunc(viewportRows) || 1);
    if (Math.abs(whole) > limit) {
      // Drop the excess instead of banking it: a clamped event should not
      // dribble out over the following ones.
      this.residual = 0;
      return whole < 0 ? -limit : limit;
    }
    return whole;
  }
}

/** Fractional rows a wheel event covers, per its deltaMode. */
export function wheelRows(ev: WheelDelta, cellHeight: number, viewportRows: number): number {
  if (!Number.isFinite(ev.deltaY) || ev.deltaY === 0) return 0;
  switch (ev.deltaMode) {
    case DOM_DELTA_LINE:
      return ev.deltaY;
    case DOM_DELTA_PAGE:
      return ev.deltaY * Math.max(1, Math.trunc(viewportRows) || 1);
    default:
      // DOM_DELTA_PIXEL. ghostty-web falls back to a 20px cell when it has no
      // metrics yet; match that rather than dividing by zero.
      return ev.deltaY / (cellHeight > 0 ? cellHeight : 20);
  }
}
