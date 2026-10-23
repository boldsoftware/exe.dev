// Small, always-on terminal flight recorder for investigating wake/reconnect
// glitches. Nothing is sent to a server or saved to storage. Never retain PTY
// bytes, input, URLs, session names, or rendered text.
import type { TerminalLike } from "./terminalRenderer";

type Detail = Partial<{
  socket: number;
  code: number;
  readyState: number;
  delayMs: number;
  cols: number;
  rows: number;
  width: number;
  height: number;
  bytes: number;
  units: number;
  ris: boolean;
  erase: boolean;
  home: boolean;
  cursorMove: boolean;
  altScreen: boolean;
  renderer: "ghostty" | "xterm";
}>;

type DebugTerm = TerminalLike & {
  buffer?: {
    active: {
      type: string;
      cursorX: number;
      cursorY: number;
      viewportY: number;
      length: number;
      getLine(y: number):
        | {
            length: number;
            getCell(x: number): { getCode(): number } | undefined;
          }
        | undefined;
    };
  };
  getViewportY?(): number;
};

type FlightRecorder = ReturnType<typeof createTerminalFlightRecorder>;

declare global {
  interface Window {
    __exeTerminalDebug?: Pick<FlightRecorder, "report">;
  }
}

// One pass over PTY bytes; record only the *shape* of repaint controls.
// Neither text nor numeric CSI parameters are retained.
function repaintControls(bytes: Uint8Array) {
  let ris = false;
  let erase = false;
  let home = false;
  let cursorMove = false;
  let altScreen = false;
  for (let i = 0; i < bytes.length - 1; i++) {
    if (bytes[i] !== 27) continue;
    if (bytes[i + 1] === 99) {
      ris = true; // ESC c
      continue;
    }
    if (bytes[i + 1] !== 91) continue;
    if (
      bytes[i + 2] === 63 &&
      bytes[i + 3] === 49 &&
      bytes[i + 4] === 48 &&
      bytes[i + 5] === 52 &&
      bytes[i + 6] === 57
    ) {
      altScreen = true; // CSI ?1049h/l
    }
    if ((bytes[i + 2] === 50 || bytes[i + 2] === 51) && bytes[i + 3] === 74) erase = true;
    if (bytes[i + 2] === 72) home = true;
    let j = i + 2;
    while (j < bytes.length && ((bytes[j] >= 48 && bytes[j] <= 57) || bytes[j] === 59)) j++;
    if (bytes[j] === 72 || bytes[j] === 102) cursorMove = true; // CSI row;col H/f
  }
  return { ris, erase, home, cursorMove, altScreen };
}

function screenState(term: TerminalLike | null) {
  if (!term) return null;
  const t = term as DebugTerm;
  try {
    const active = t.buffer?.active;
    if (!active) return { cols: term.cols, rows: term.rows };
    const bottom = Math.max(0, active.length - term.rows);
    // ghostty-web measures viewportY as lines scrolled *back* from the
    // bottom; xterm.js exposes its absolute buffer index instead.
    const start =
      typeof t.getViewportY === "function"
        ? Math.max(0, bottom - Math.floor(t.getViewportY()))
        : Math.max(0, Math.min(bottom, active.viewportY));
    const contentRows: number[] = [];
    for (let row = 0; row < Math.min(term.rows, 500); row++) {
      const line = active.getLine(start + row);
      if (!line) continue;
      for (let col = 0; col < Math.min(term.cols, line.length); col++) {
        const code = line.getCell(col)?.getCode() ?? 0;
        if (code > 32 && code !== 127) {
          contentRows.push(row + 1);
          break;
        }
      }
    }
    return {
      cols: term.cols,
      rows: term.rows,
      screen: active.type,
      cursor: [active.cursorX + 1, active.cursorY + 1],
      scrollbackLines: Math.max(0, active.length - term.rows),
      viewportOffset: bottom - start,
      // Row numbers only, NOT the contents of those rows. Compare with a
      // screenshot to distinguish old buffer cells from stale canvas pixels.
      contentRows,
    };
  } catch {
    return { cols: term.cols, rows: term.rows, unavailable: true };
  }
}

function canvasState(host: HTMLElement | null) {
  const canvas = host?.querySelector("canvas");
  if (!canvas) return null;
  const rect = canvas.getBoundingClientRect();
  return {
    pixels: [canvas.width, canvas.height],
    displayed: [Math.round(rect.width), Math.round(rect.height)],
  };
}

export function createTerminalFlightRecorder(
  surface: string,
  state: () => {
    host: HTMLElement | null;
    term: TerminalLike | null;
    renderer: string;
    ws: WebSocket | null;
    connected: boolean;
    status: string;
    statusVisible: boolean;
  },
) {
  const events: { at: number; event: string; detail?: Detail }[] = [];
  let lastOutputAt = 0;
  let lastTrafficAt = 0;
  let outputBytes = 0;
  let outputFrames = 0;
  let lastInputAt = 0;
  let inputUnits = 0;
  let socketInputs = 0;
  let socketNumber = 0;
  let socketFrames = 0;

  function record(event: string, detail?: Detail) {
    events.push({ at: Date.now(), event, ...(detail ? { detail } : {}) });
    if (events.length > 96) events.shift();
  }

  function socket() {
    socketNumber++;
    socketFrames = 0;
    socketInputs = 0;
    record("socket-created", { socket: socketNumber });
  }

  function input(units: number) {
    lastInputAt = Date.now();
    inputUnits += units;
    if (socketInputs++ === 0) record("first-input", { socket: socketNumber, units });
  }

  function output(bytes: Uint8Array) {
    lastOutputAt = lastTrafficAt = Date.now();
    outputBytes += bytes.length;
    outputFrames++;
    socketFrames++;
    // A sequence split across WebSocket frames is intentionally not retained:
    // even an eight-byte tail could contain the user's actual shell output.
    const { ris, erase, home, cursorMove, altScreen } = repaintControls(bytes);
    // Record the beginning of each attach/reconnect and later repaint
    // controls, not every chunk of a long-running `cat` or TUI.
    if (socketFrames <= 12 || ris || erase || altScreen) {
      record("output", {
        socket: socketNumber,
        bytes: bytes.length,
        ris,
        erase,
        home,
        cursorMove,
        altScreen,
      });
    }
  }

  function pong() {
    lastTrafficAt = Date.now();
    record("pong");
  }

  function report() {
    const { host, term, renderer, ws, connected, status, statusVisible } = state();
    const rect = host?.getBoundingClientRect();
    const viewport = host?.querySelector(".xterm-viewport");
    const visual = window.visualViewport;
    return {
      version: 1,
      surface,
      capturedAt: new Date().toISOString(),
      renderer,
      connection: {
        connected,
        status,
        statusVisible,
        readyState: ws?.readyState ?? null,
        socket: socketNumber,
        inputCodeUnits: inputUnits,
        lastInputAgoMs: lastInputAt ? Date.now() - lastInputAt : null,
        outputFrames,
        outputBytes,
        lastTrafficAgoMs: lastTrafficAt ? Date.now() - lastTrafficAt : null,
        lastOutputAgoMs: lastOutputAt ? Date.now() - lastOutputAt : null,
      },
      browser: {
        online: navigator.onLine,
        visibility: document.visibilityState,
        focused: document.hasFocus(),
        width: innerWidth,
        height: innerHeight,
        dpr: devicePixelRatio,
        visualViewport: visual
          ? { width: visual.width, height: visual.height, scale: visual.scale }
          : null,
      },
      host: rect ? [Math.round(rect.width), Math.round(rect.height)] : null,
      canvas: canvasState(host),
      xtermScroll: viewport
        ? { top: viewport.scrollTop, height: viewport.scrollHeight, visible: viewport.clientHeight }
        : null,
      terminal: screenState(term),
      events: events.slice(),
    };
  }

  const consoleHandle = { report };
  window.__exeTerminalDebug = consoleHandle;
  const onVisibility = () => record(document.hidden ? "hidden" : "visible");
  const onOnline = () => record("online");
  const onOffline = () => record("offline");
  const onFocus = () => record("focus");
  const onResize = () => record("window-resize", { width: innerWidth, height: innerHeight });
  const onPageShow = (event: PageTransitionEvent) =>
    record(event.persisted ? "pageshow-bfcache" : "pageshow");
  const onPageHide = (event: PageTransitionEvent) =>
    record(event.persisted ? "pagehide-bfcache" : "pagehide");
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("online", onOnline);
  window.addEventListener("offline", onOffline);
  window.addEventListener("focus", onFocus);
  window.addEventListener("resize", onResize);
  window.addEventListener("pageshow", onPageShow);
  window.addEventListener("pagehide", onPageHide);
  record("mounted", { width: innerWidth, height: innerHeight });

  function dispose() {
    record("unmounted");
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("online", onOnline);
    window.removeEventListener("offline", onOffline);
    window.removeEventListener("focus", onFocus);
    window.removeEventListener("resize", onResize);
    window.removeEventListener("pageshow", onPageShow);
    window.removeEventListener("pagehide", onPageHide);
    if (window.__exeTerminalDebug === consoleHandle) delete window.__exeTerminalDebug;
  }

  return { record, socket, input, output, pong, report, dispose };
}
