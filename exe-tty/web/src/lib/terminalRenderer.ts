// Runtime-toggleable terminal backend.
//
// Two implementations are wired up:
//   - 'ghostty' (default) — `ghostty-web`, Ghostty's VT100 emulator compiled
//      to WASM. xterm.js-compatible API surface.
//   - 'xterm'             — the legacy `@xterm/xterm` backend.
//
// Selection is runtime-only (no rebuild needed):
//   1. ?renderer=ghostty|xterm  in the page URL (persists into localStorage)
//   2. localStorage['exe:terminal:renderer']
//   3. <meta name="exe-terminal-renderer" content="...">
//   4. default: 'ghostty'
//
// Both backends expose the small subset of the API the two terminal views
// actually use (constructor options, open, write, onData, focus, dispose,
// attachCustomKeyEventHandler, loadAddon, options.theme, cols, rows) plus a
// FitAddon with .fit()/.proposeDimensions() and a WebLinksAddon stub.
//
// The ghostty backend also gets its wheel handling corrected here — see
// installGhosttyWheelPolicy() and ./terminalScrollPolicy.
//
// See README in this file's comment block for verification steps.

import {
  cursorKeyBytes,
  mouseWheelBytes,
  terminalScrollGesture,
  WheelRowAccumulator,
  type MouseReportFormat,
  type TerminalScrollModes,
} from "./terminalScrollPolicy";

export type RendererKind = "ghostty" | "xterm";

const STORAGE_KEY = "exe:terminal:renderer";

export function getRendererKind(): RendererKind {
  try {
    const params = new URLSearchParams(window.location.search);
    const fromQuery = params.get("renderer");
    if (fromQuery === "ghostty" || fromQuery === "xterm") {
      try {
        localStorage.setItem(STORAGE_KEY, fromQuery);
      } catch {
        /* ignore */
      }
      return fromQuery;
    }
  } catch {
    /* ignore */
  }
  try {
    const fromStorage = localStorage.getItem(STORAGE_KEY);
    if (fromStorage === "ghostty" || fromStorage === "xterm") return fromStorage;
  } catch {
    /* ignore */
  }
  try {
    const meta = document.querySelector(
      'meta[name="exe-terminal-renderer"]',
    ) as HTMLMetaElement | null;
    const v = meta?.content;
    if (v === "ghostty" || v === "xterm") return v;
  } catch {
    /* ignore */
  }
  return "ghostty";
}

export function setRendererKind(kind: RendererKind) {
  try {
    localStorage.setItem(STORAGE_KEY, kind);
  } catch {
    /* ignore */
  }
  // getRendererKind() ranks a ?renderer= query param ABOVE localStorage, so a
  // page opened with ?renderer=... would make a runtime toggle a silent no-op
  // on reload (the stale param would win). Keep the URL in sync so an explicit
  // toggle actually persists. Only touch the param when it's already present;
  // don't add it to clean URLs.
  try {
    const url = new URL(window.location.href);
    if (url.searchParams.has("renderer")) {
      url.searchParams.set("renderer", kind);
      window.history.replaceState(window.history.state, "", url);
    }
  } catch {
    /* ignore (no window/history in tests) */
  }
}

// What to advertise as the PTY $TERM for a given backend. Programs check TERM
// to decide what they can draw; with ghostty-web we can honestly advertise
// xterm-ghostty (the terminfo entry that ships with Ghostty), which also flips
// on its native keyboard protocol, OSC 8 hyperlinks, etc. in well-behaved
// apps.
export function termNameFor(kind: RendererKind): string {
  return kind === "ghostty" ? "xterm-ghostty" : "xterm-256color";
}

// Minimal shared interfaces, common to both backends.
export interface TerminalLike {
  readonly cols: number;
  readonly rows: number;
  options: { theme?: unknown; [k: string]: unknown };
  element?: HTMLElement;
  loadAddon(addon: unknown): void;
  open(parent: HTMLElement): void;
  focus(): void;
  write(data: string | Uint8Array): void;
  paste(data: string): void;
  hasSelection(): boolean;
  getSelection(): string;
  clearSelection(): void;
  onData(cb: (data: string) => void): { dispose(): void } | void;
  attachCustomKeyEventHandler(cb: (ev: KeyboardEvent) => boolean): void;
  resize(cols: number, rows: number): void;
  dispose(): void;
}

export interface FitAddonLike {
  fit(): void;
  proposeDimensions(): { cols: number; rows: number } | undefined;
}

export interface TerminalCtor {
  new (opts: Record<string, unknown>): TerminalLike;
}
export interface FitAddonCtor {
  new (): FitAddonLike;
}
export interface WebLinksAddonCtor {
  new (): unknown;
}

export interface TerminalBackend {
  kind: RendererKind;
  Terminal: TerminalCtor;
  FitAddon: FitAddonCtor;
  WebLinksAddon: WebLinksAddonCtor;
}

// Caches per kind so toggling back and forth doesn't reinit anything.
// `ghostty-web.init()` is itself idempotent (a module-level promise), but we
// cache the whole backend per-kind regardless to avoid re-parsing modules.
const backendCache = new Map<RendererKind, Promise<TerminalBackend>>();

export function loadTerminalBackend(forceKind?: RendererKind): Promise<TerminalBackend> {
  const kind = forceKind ?? getRendererKind();
  const hit = backendCache.get(kind);
  if (hit) return hit;
  const p: Promise<TerminalBackend> = (async (): Promise<TerminalBackend> => {
    if (kind === "ghostty") {
      try {
        const [mod, { default: wasmUrl }] = await Promise.all([
          import("ghostty-web"),
          // Import the WASM as a hashed, cacheable URL rather than letting
          // ghostty-web inline it as a base64 data: URL. The Vite build
          // emits assets/ghostty-vt-<hash>.wasm and a companion plugin
          // strips the dead inline blob from the ghostty-web chunk. Serving
          // it same-origin (no data: URL) also keeps the terminal origin's
          // connect-src CSP tight. See vite.config.ts (ghostty-wasm plugin).
          import("ghostty-web/ghostty-vt.wasm?url"),
        ]);
        // ghostty-web's init() loads WASM from its inline data: URL; we load
        // from the hashed asset URL instead and share the instance across
        // every Terminal via the constructor's { ghostty } option.
        const ghostty = await mod.Ghostty.load(wasmUrl);
        const GhosttyTerminal = mod.Terminal as unknown as {
          prototype: TerminalLike;
        } & TerminalCtor;
        patchGhosttyPrototype(GhosttyTerminal.prototype);
        // Callers do `new backend.Terminal(opts)` without knowing about the
        // shared ghostty instance; bind it in via a thin subclass so the
        // pre-loaded WASM is injected transparently.
        class BoundGhosttyTerminal extends (GhosttyTerminal as unknown as {
          new (opts: Record<string, unknown>): TerminalLike;
        }) {
          constructor(opts: Record<string, unknown>) {
            super({ ...opts, ghostty });
          }
        }
        return {
          kind: "ghostty" as const,
          Terminal: BoundGhosttyTerminal as unknown as TerminalCtor,
          FitAddon: mod.FitAddon as unknown as FitAddonCtor,
          // ghostty-web has no separate web-links addon — links are detected
          // built-in via UrlRegexProvider. Provide a no-op stub so callers
          // can blindly `loadAddon(new WebLinksAddon())`.
          WebLinksAddon: class WebLinksAddonStub {
            activate() {
              /* no-op */
            }
            dispose() {
              /* no-op */
            }
          } as unknown as WebLinksAddonCtor,
        };
      } catch (err) {
        console.warn(
          "[terminalRenderer] ghostty-web failed to load, falling back to xterm.js",
          err,
        );
        // Reuse the xterm backend's cache entry too so a subsequent caller
        // asking for 'xterm' doesn't reload it.
        let xtermP = backendCache.get("xterm");
        if (!xtermP) {
          xtermP = loadXterm().catch((e) => {
            backendCache.delete("xterm");
            throw e;
          });
          backendCache.set("xterm", xtermP);
        }
        return xtermP;
      }
    }
    return loadXterm();
  })().catch((e) => {
    // Don't pin a rejected promise in the cache — a later mount may succeed
    // (e.g. after a transient network blip during code-split load).
    backendCache.delete(kind);
    throw e;
  });
  backendCache.set(kind, p);
  return p;
}

// Compatibility policies are installed from prototype hooks so they apply
// to every Terminal the app constructs. Exported for regression tests.
const PATCHED = Symbol.for("exe.ghosttyTerminalPatched");
const WHEEL_POLICY_INSTALLED = Symbol.for("exe.ghosttyWheelPolicy");

// DEC private modes the scroll decision reads. Numbers, not names, because
// that's what ghostty-web's getMode() takes.
const DEC_CURSOR_KEYS_APPLICATION = 1; // DECCKM
const DEC_ALTERNATE_SCROLL = 1007;
const DEC_MOUSE_SGR = 1006;
const DEC_MOUSE_URXVT = 1015;
const DEC_MOUSE_SGR_PIXELS = 1016;

// The ghostty-web Terminal surface the wheel policy needs. All optional: a
// backend that predates one of these (or a test double) degrades to "leave the
// wheel alone" rather than throwing inside an event handler.
export interface GhosttyWheelTerminal {
  readonly cols: number;
  readonly rows: number;
  element?: HTMLElement;
  attachCustomWheelEventHandler?(cb?: (ev: WheelEvent) => boolean): void;
  input?(data: string, wasUserInput?: boolean): void;
  getMode?(mode: number, isAnsi?: boolean): boolean;
  hasMouseTracking?(): boolean;
  buffer?: { active?: { type?: string } };
  wasmTerm?: { isAlternateScreen?(): boolean };
}

// Every read here goes through the emulator, which throws from assertOpen()
// before open() and after dispose() — and this runs inside a DOM event
// handler, where a throw would leave the page with a half-handled wheel tick.
function safely<T>(read: () => T, fallback: T): T {
  try {
    const v = read();
    return v === undefined ? fallback : v;
  } catch {
    return fallback;
  }
}

export function readGhosttyScrollModes(term: GhosttyWheelTerminal): TerminalScrollModes {
  const mode = (n: number, fallback: boolean) =>
    term.getMode ? safely(() => term.getMode!(n, false), fallback) : fallback;
  return {
    // buffer.active.type is the public spelling; wasmTerm.isAlternateScreen()
    // is the same bit one layer down, kept as a fallback because the buffer
    // namespace is lazily built.
    onAlternateScreen:
      safely(() => term.buffer?.active?.type, undefined) === "alternate" ||
      safely(() => term.wasmTerm?.isAlternateScreen?.(), false) === true,
    mouseTrackingActive: term.hasMouseTracking
      ? safely(() => term.hasMouseTracking!(), false)
      : false,
    // 1007 defaults to ON in ghostty (terminal/modes.zig), and the fallback
    // matters: defaulting it off would swallow the wheel in `less` on any
    // backend that can't answer.
    alternateScrollEnabled: mode(DEC_ALTERNATE_SCROLL, true),
    cursorKeysApplicationMode: mode(DEC_CURSOR_KEYS_APPLICATION, false),
  };
}

function mouseReportFormat(term: GhosttyWheelTerminal): MouseReportFormat {
  const mode = (n: number) => (term.getMode ? safely(() => term.getMode!(n, false), false) : false);
  // Order matters: an app that sets both 1006 and 1016 wants pixels.
  if (mode(DEC_MOUSE_SGR_PIXELS)) return "sgrPixels";
  if (mode(DEC_MOUSE_SGR)) return "sgr";
  if (mode(DEC_MOUSE_URXVT)) return "urxvt";
  return "x10";
}

// Where in the grid the pointer is, plus the cell height the row quantization
// needs. ghostty sizes its canvas to exactly cols×rows cells, so the canvas
// rect divides evenly; the host element can be larger (letterboxing) and would
// skew the mapping, hence the querySelector.
function wheelGeometry(term: GhosttyWheelTerminal, ev: WheelEvent) {
  const host = term.element;
  const canvas = host?.querySelector("canvas") ?? host;
  const rect = canvas?.getBoundingClientRect();
  const cols = Math.max(1, term.cols || 1);
  const rows = Math.max(1, term.rows || 1);
  if (!rect || rect.width <= 0 || rect.height <= 0) {
    return { col: 1, row: 1, x: 1, y: 1, cellHeight: 0, rows };
  }
  const cellWidth = rect.width / cols;
  const cellHeight = rect.height / rows;
  // Synthesized wheel events (the touch-drag path in Terminal.vue) carry no
  // pointer position; report the top-left cell rather than NaN coordinates,
  // which would decode as a random cell — apps that scroll from the wheel
  // ignore the position anyway.
  const at = (v: number) => (Number.isFinite(v) ? v : 0);
  const x = at(ev.clientX) - rect.left;
  const y = at(ev.clientY) - rect.top;
  const clamp = (v: number, hi: number) => Math.min(Math.max(v, 1), hi);
  return {
    col: clamp(Math.floor(x / cellWidth) + 1, cols),
    row: clamp(Math.floor(y / cellHeight) + 1, rows),
    x: Math.max(1, Math.round(x)),
    y: Math.max(1, Math.round(y)),
    cellHeight,
    rows,
  };
}

/**
 * Give the wheel to whoever asked for it.
 *
 * ghostty-web's own handleWheel() turns every wheel tick on the alternate
 * screen into ESC[A/ESC[B without asking whether the application enabled mouse
 * reporting, so a mouse-reporting TUI (Claude Code, tmux `mouse on`, htop) gets
 * arrow keys it never wanted — they walk the prompt instead of scrolling the
 * view. handleWheel() consults customWheelEventHandler first and returns early
 * when it's truthy, which is the seam this uses: decide with
 * terminalScrollGesture(), send the bytes ourselves, and only fall through to
 * ghostty for the case it gets right (its own scrollback, smooth-scrolled).
 *
 * Returns whether the handler was installed. Exported for unit tests.
 */
export function installGhosttyWheelPolicy(term: GhosttyWheelTerminal): boolean {
  const marked = term as GhosttyWheelTerminal & {
    [WHEEL_POLICY_INSTALLED]?: boolean;
  };
  if (marked[WHEEL_POLICY_INSTALLED]) return false;
  if (!term.attachCustomWheelEventHandler || !term.input) return false;

  const accumulator = new WheelRowAccumulator();
  term.attachCustomWheelEventHandler((ev: WheelEvent): boolean => {
    const modes = readGhosttyScrollModes(term);
    // The .viewport branch of the policy: primary screen, no mouse reporting.
    // Hand it back to ghostty, whose viewport scroller is fractional (smooth)
    // and knows about its own scrollbar. Checked before quantizing so the
    // accumulator never banks rows it didn't emit.
    if (!modes.mouseTrackingActive && !modes.onAlternateScreen) {
      accumulator.reset();
      return false;
    }
    const geo = wheelGeometry(term, ev);
    const rows = accumulator.consume(ev, geo.cellHeight, geo.rows);
    const gesture = terminalScrollGesture(rows, modes);
    switch (gesture.kind) {
      case "mouseWheel":
        term.input!(
          mouseWheelBytes({
            count: gesture.count,
            up: gesture.up,
            format: mouseReportFormat(term),
            col: geo.col,
            row: geo.row,
            x: geo.x,
            y: geo.y,
          }),
          true,
        );
        return true;
      case "cursorKeys":
        term.input!(cursorKeyBytes(gesture.count, gesture.up, gesture.applicationMode), true);
        return true;
      case "viewport":
        // Unreachable: the early return above covers every viewport case.
        // Falling through to ghostty is the safe reading of it regardless.
        accumulator.reset();
        return false;
      case "none":
        // Either sub-row travel we've banked, or an app that wants neither
        // reports nor arrows. Either way this tick is ours, not ghostty's.
        return true;
    }
  });
  Object.defineProperty(marked, WHEEL_POLICY_INSTALLED, { value: true });
  return true;
}

/**
 * Whether the application has turned on mouse reporting, so pointer gestures
 * in the terminal go to it rather than to the emulator's own selection.
 * ghostty-web answers directly; xterm.js exposes the same bit through modes.
 */
export function terminalReportsMouse(term: TerminalLike): boolean {
  const t = term as TerminalLike & {
    hasMouseTracking?: () => boolean;
    modes?: { mouseTrackingMode?: string };
  };
  if (typeof t.hasMouseTracking === "function") {
    return safely(() => t.hasMouseTracking!(), false);
  }
  const mode = t.modes?.mouseTrackingMode;
  return typeof mode === "string" && mode !== "none";
}

// The exact list xterm.js uses (common/Platform.ts), so the two agree on which
// modifier forces a local selection. macOS also turns Control+primary into a
// context click.
export const isMacPlatform = ["Macintosh", "MacIntel", "MacPPC", "Mac68K"].includes(
  typeof navigator === "undefined" ? "" : navigator.platform,
);

/**
 * Whether a primary-button press with these modifiers is kept by the emulator
 * for its own selection instead of being reported to a mouse-tracking app.
 * ghostty-web: Shift on every platform (installGhosttySelectionPolicy).
 * xterm.js: Shift, except on macOS where SelectionService.shouldForceSelection
 * reads Option (see macOptionClickForcesSelection where the Terminal is built).
 */
export function terminalKeepsGestureLocal(kind: RendererKind, ev: MouseEvent): boolean {
  if (kind === "xterm" && isMacPlatform) return ev.altKey;
  return ev.shiftKey;
}

// The seam the selection policy uses: ghostty-web's InputHandler gates every
// mouse report on mouseConfig.hasMouseTracking(). Neither field is part of the
// public API, so all of them are optional and a backend without them simply
// gets no policy.
export interface GhosttyMouseTerminal {
  element?: HTMLElement;
  inputHandler?: {
    mouseConfig?: { hasMouseTracking?: () => boolean };
    sendMouseEvent?: (
      button: number,
      col: number,
      row: number,
      release: boolean,
      ev?: MouseEvent,
    ) => void;
  };
  selectionManager?: { clearSelection?: () => void; isSelecting?: boolean };
}

/**
 * Selection versus mouse tracking, the way desktop terminals do it: while an
 * application has mouse reporting on, a plain gesture belongs to the
 * application and Shift+drag (or Shift+double-click) is the terminal's own
 * selection.
 *
 * ghostty-web@0.4.0-next.14 does neither. Its SelectionManager starts a
 * selection on every drag, but the InputHandler reports the same
 * mousedown/mousemove/mouseup to the application, and each report goes
 * through Terminal.onDataCallback, which clears the selection -- so a drag in
 * a mouse-tracking app selects nothing, Shift or no Shift. Its double- and
 * triple-click handlers, on the other hand, always select and copy a word or
 * line, even though the application received the same clicks and may be
 * selecting and copying (via OSC 52) on its own; the two writes then race for
 * the clipboard with different text.
 *
 * So, while the application tracks the mouse:
 *  - With Shift held on the primary press, the InputHandler's tracking gate
 *    answers false until the release, so no report is generated and the
 *    selection survives to mouseup, where ghostty's copy-on-select handles
 *    it. The flag is set from a capture listener on the host and cleared from
 *    a bubble listener on the document: the InputHandler's listeners sit
 *    between those two points in the event path, so the press, the drag and
 *    the release itself are all hidden from it (a lone release report would
 *    clear the selection just the same). A release the document never sees --
 *    outside the window, or swallowed upstream -- is caught up with on the
 *    next motion with the button up.
 *  - Without Shift, multi-click events are stopped at the host before they
 *    reach the SelectionManager's canvas listener, so ghostty neither selects
 *    nor copies; the application does, if it wants to.
 *  - Mouse reports go through the same data callback as typing, which clears
 *    the selection. Only a primary press keeps that effect: an application
 *    with any-event tracking (Claude Code) hears about every motion, and a
 *    selection that vanished on the first mouse movement could never reach
 *    the right-click that copies it.
 * Terminal.hasMouseTracking() is left truthful; only the InputHandler's view
 * changes.
 *
 * Returns a disposer, or null when the backend lacks the seam.
 */
export function installGhosttySelectionPolicy(term: GhosttyMouseTerminal): (() => void) | null {
  const host = term.element;
  const gate = term.inputHandler?.mouseConfig;
  if (!host || !gate || typeof gate.hasMouseTracking !== "function") return null;

  const original = gate.hasMouseTracking;
  const tracking = () => original.call(gate);
  let nativeGesture = false;

  const handler = term.inputHandler!;
  const selection = term.selectionManager;
  const send = handler.sendMouseEvent;
  const clear = selection?.clearSelection;
  let keepSelection = false;
  if (send && clear) {
    handler.sendMouseEvent = function (button, col, row, release, ev) {
      keepSelection = button !== 0 || release;
      try {
        send.call(this, button, col, row, release, ev);
      } finally {
        keepSelection = false;
      }
    };
    selection.clearSelection = function () {
      if (keepSelection) return;
      clear.call(this);
      // A press report arrives while the SelectionManager's own selection is
      // still collapsed on the pressed cell, which clearSelection() ignores;
      // the motion reports used to end that gesture, so end it here instead.
      this.isSelecting = false;
    };
  }
  // Whether the press behind the current click sequence carried Shift; the
  // click event arrives after mouseup has already cleared nativeGesture.
  let pressWasLocal = false;
  gate.hasMouseTracking = () => !nativeGesture && tracking();

  const onMouseDown = (ev: MouseEvent) => {
    if (ev.button !== 0) return;
    pressWasLocal = ev.shiftKey;
    nativeGesture = ev.shiftKey && tracking();
  };
  const onMouseUp = (ev: MouseEvent) => {
    if (ev.button === 0) nativeGesture = false;
  };
  const onMouseMove = (ev: MouseEvent) => {
    if (nativeGesture && !(ev.buttons & 1)) nativeGesture = false;
  };
  const onBlur = () => {
    nativeGesture = false;
  };
  const onClick = (ev: MouseEvent) => {
    if (ev.detail >= 2 && !pressWasLocal && tracking()) ev.stopPropagation();
  };
  host.addEventListener("mousedown", onMouseDown, { capture: true });
  host.addEventListener("click", onClick, { capture: true });
  document.addEventListener("mouseup", onMouseUp);
  document.addEventListener("mousemove", onMouseMove);
  window.addEventListener("blur", onBlur);
  return () => {
    host.removeEventListener("mousedown", onMouseDown, { capture: true });
    host.removeEventListener("click", onClick, { capture: true });
    document.removeEventListener("mouseup", onMouseUp);
    document.removeEventListener("mousemove", onMouseMove);
    window.removeEventListener("blur", onBlur);
    nativeGesture = false;
    gate.hasMouseTracking = original;
    if (send && clear) {
      handler.sendMouseEvent = send;
      selection.clearSelection = clear;
    }
  };
}

const SELECTION_POLICY_DISPOSER = Symbol.for("exe.ghosttySelectionPolicy");

// xterm.js's private Terminal, reached the way its own addons do (_core).
export interface XtermMouseTerminal {
  _core?: {
    coreService?: { triggerDataEvent?: (data: string, wasUserInput?: boolean) => void };
  };
}

// An SGR mouse report for anything but a primary press: motion, wheel, other
// buttons, releases. Modifier bits (4, 8, 16) do not make a press less of one.
const SGR_NON_PRESS_REPORT = (data: string) => {
  const m = /^\x1b\[<(\d+);\d+;\d+([Mm])$/.exec(data);
  return !!m && (m[2] === "m" || (Number(m[1]) & ~28) !== 0);
};

/**
 * The xterm.js half of the selection-versus-reports rule above: its
 * SelectionService clears on every user-input data event, and every SGR
 * mouse report is one. Pass everything but a primary press as plain data.
 * Returns false when the internals have moved.
 */
export function installXtermSelectionPolicy(term: XtermMouseTerminal): boolean {
  const core = term._core?.coreService;
  const emit = core?.triggerDataEvent;
  if (!core || !emit) return false;
  core.triggerDataEvent = function (data, wasUserInput) {
    emit.call(this, data, wasUserInput && !SGR_NON_PRESS_REPORT(data));
  };
  return true;
}

// The pinned ghostty-web parses DEC2026, but CanvasRenderer ignores it and
// clears the WASM dirty state on every render. Gate that single paint entry
// point, NOT writes: parsing, generated replies, and input must keep flowing.
// This uses parsed mode state (including split byte sequences), not a second
// escape-sequence parser. DECRQM replies remain the upstream engine's concern.
// renderer.render is the exported CanvasRenderer's common paint entry point;
// the pinned package also exposes renderer to its own FitAddon. Browser tests
// exercise this dependency seam against the real package on every opt-in run.
export const GHOSTTY_SYNC_TIMEOUT_MS = 1000;

export interface GhosttySynchronizedTerminal {
  getMode?(mode: number, isAnsi?: boolean): boolean;
  write(data: string | Uint8Array, callback?: () => void): void;
  resize?(cols: number, rows: number): void;
  readonly cols?: number;
  readonly rows?: number;
  renderer?: {
    render(buffer: unknown, force?: boolean, ...rest: unknown[]): void;
  };
}

const synchronizedOutputCleanup = new WeakMap<object, () => void>();

export function installGhosttySynchronizedOutput(term: GhosttySynchronizedTerminal): boolean {
  const renderer = term.renderer;
  if (!renderer || !term.getMode || synchronizedOutputCleanup.has(term)) return false;
  const render = renderer.render;
  const write = term.write;
  const resize = term.resize;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let active = false;
  let expired = false;
  let force = false;
  let disposed = false;
  let lastArgs: Parameters<typeof render> | undefined;

  const cancelTimer = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  // Fail open for the rest of this held mode after the deadline. Do not
  // repeatedly hide output for another second, or change the emulator's mode.
  const release = () => {
    cancelTimer();
    expired = true;
    force = true;
  };
  const shouldHold = () => {
    const enabled = safely(() => term.getMode!(2026, false), false);
    if (enabled && !active) {
      active = true;
      expired = false;
      force = true;
      timer = setTimeout(() => {
        if (disposed) return;
        release();
        if (lastArgs) renderer.render(...lastArgs);
      }, GHOSTTY_SYNC_TIMEOUT_MS);
    } else if (!enabled && active) {
      cancelTimer();
      active = false;
      expired = false;
      force = true;
    }
    return active && !expired;
  };

  renderer.render = function (...args) {
    if (disposed) return;
    lastArgs = args;
    if (shouldHold()) return; // Leave WASM dirty state untouched.
    render.call(this, args[0], args[1] || force, ...args.slice(2));
    force = false;
  };
  term.write = function (data, callback) {
    write.call(this, data, callback);
    // Observe resets even if a new update begins before the next RAF. This
    // also starts the deadline at parsing time, not the next visible frame.
    shouldHold();
  };
  if (resize) {
    term.resize = function (cols, rows) {
      // Resizing clears the canvas outside render(). End the visual hold so
      // it repaints immediately rather than leaving a blank resized canvas.
      if (active && (cols !== term.cols || rows !== term.rows)) release();
      resize.call(this, cols, rows);
    };
  }
  synchronizedOutputCleanup.set(term, () => {
    disposed = true;
    cancelTimer();
    lastArgs = undefined;
    renderer.render = render;
    term.write = write;
    if (resize) term.resize = resize;
    synchronizedOutputCleanup.delete(term);
  });
  return true;
}

const inputFocusCleanup = new WeakMap<object, () => void>();

function installGhosttyInputFocus(term: TerminalLike, host: HTMLElement) {
  // In the pinned package both Terminal.focus() and SelectionManager focus
  // this contenteditable host, but beforeinput is handled ONLY on its hidden
  // textarea. ASCII keydown works on either; Unicode/IME insertText is lost
  // on the host because its beforeinput listener just preventDefaults it.
  const input = host.querySelector("textarea");
  if (!input) return;
  const redirect = () => {
    if (host.ownerDocument.activeElement === host) input.focus({ preventScroll: true });
  };
  // focus does not bubble: focusing the textarea cannot recurse here. Only
  // redirect an existing focus intent, never wheel/touchmove/scroll events.
  host.addEventListener("focus", redirect);
  inputFocusCleanup.set(term, () => host.removeEventListener("focus", redirect));
  redirect(); // open() may have already focused the host before our hook ran.
}

export function patchGhosttyPrototype(prototype: TerminalLike) {
  const proto = prototype as unknown as {
    attachCustomKeyEventHandler: TerminalLike["attachCustomKeyEventHandler"];
    open: TerminalLike["open"];
    dispose: TerminalLike["dispose"];
    [PATCHED]?: boolean;
  };
  // Double-patching would double-invert attachCustomKeyEventHandler, silently
  // breaking every key handler, so guard hard. The marker is a non-enumerable
  // registry symbol: it can't collide with ghostty-web's own fields and won't
  // show up in for..in over instances.
  if (proto[PATCHED]) return;

  // ghostty-web inverts the semantics of attachCustomKeyEventHandler: a
  // truthy return prevents the terminal from handling the key, while xterm.js
  // uses truthy as "let the terminal handle it." The terminal views in this
  // app are written against xterm.js semantics (they `return true` to fall
  // through to the terminal). Patch the prototype so callers see the xterm.js
  // contract regardless of backend. See ghostty-web@0.4.0
  // InputHandler.handleKeyDown.
  const attach = proto.attachCustomKeyEventHandler;
  proto.attachCustomKeyEventHandler = function (this: TerminalLike, cb) {
    return attach.call(this, (ev) => !cb(ev));
  };

  // ghostty-web's open() marks the host element contenteditable="true" (for
  // IME/beforeinput), which makes the browser paint its own native caret on
  // top of the canvas-drawn terminal cursor — a visible "double cursor" (a
  // thin bar parked at the element's start, plus the real block cursor).
  // Ghostty draws the only cursor that matters, so hide the native one.
  const open = proto.open;
  proto.open = function (this: TerminalLike, parent: HTMLElement) {
    const ret = open.call(this, parent);
    try {
      parent.style.caretColor = "transparent";
    } catch {
      /* ignore */
    }
    // Per-instance, and only after open(): the emulator's mode getters assert
    // an open terminal, and the canvas the geometry measures doesn't exist
    // until now.
    try {
      installGhosttyWheelPolicy(this as unknown as GhosttyWheelTerminal);
      installGhosttySynchronizedOutput(this as unknown as GhosttySynchronizedTerminal);
      installGhosttyInputFocus(this, parent);
    } catch (err) {
      console.warn("[terminalRenderer] Ghostty rendering/input policy not installed", err);
    }
    // Per instance, guarded like the wheel policy: a second open() must not
    // wrap the gate twice and leave dispose() restoring the first wrapper.
    const marked = this as TerminalLike & { [SELECTION_POLICY_DISPOSER]?: () => void };
    if (!marked[SELECTION_POLICY_DISPOSER]) {
      try {
        const dispose = installGhosttySelectionPolicy(this as unknown as GhosttyMouseTerminal);
        if (dispose) {
          Object.defineProperty(marked, SELECTION_POLICY_DISPOSER, {
            value: dispose,
            configurable: true,
          });
        }
      } catch (err) {
        console.warn("[terminalRenderer] selection policy not installed", err);
      }
    }
    return ret;
  };

  // Both terminal views reuse the same host element across a backend toggle
  // (destroy + remount into the same ref), and ghostty's dispose() only undoes
  // the attributes it set. Drop our inline style too so switching to xterm.js
  // doesn't inherit an invisible caret.
  const dispose = proto.dispose;
  proto.dispose = function (this: TerminalLike) {
    const el = this.element;
    const marked = this as TerminalLike & { [SELECTION_POLICY_DISPOSER]?: () => void };
    try {
      marked[SELECTION_POLICY_DISPOSER]?.();
      delete marked[SELECTION_POLICY_DISPOSER];
    } catch {
      /* ignore */
    }
    synchronizedOutputCleanup.get(this)?.();
    inputFocusCleanup.get(this)?.();
    inputFocusCleanup.delete(this);
    // ghostty-web@0.4.0-next.14 registers this listener with capture:true,
    // but cleanupComponents() removes it without the capture flag. On a
    // reused host the disposed terminal then swallows wheels before they
    // reach the next backend (notably xterm's descendant screen element).
    // handleWheel is an instance field in the pinned package; keep this
    // optional so a future upstream cleanup fix needs no matching shim.
    const handleWheel = (this as TerminalLike & { handleWheel?: (ev: WheelEvent) => void })
      .handleWheel;
    try {
      dispose.call(this);
    } finally {
      if (handleWheel) el?.removeEventListener("wheel", handleWheel, true);
      try {
        el?.style.removeProperty("caret-color");
      } catch {
        /* ignore */
      }
    }
  };

  Object.defineProperty(proto, PATCHED, { value: true });
}

// linkHandler is the Terminal option that decides what a click on a link
// does. Pass it wherever a Terminal is constructed: without it xterm.js
// handles OSC 8 links (which programs emit for long URLs they ask people to
// open) with a blocking confirm() warning the link "could
// potentially be dangerous". These links are ones this server just printed,
// so the warning is both alarming and wrong, and it is only reachable via
// OSC 8 — xterm's WebLinksAddon, which handles URLs it found by scanning
// text, opens them without asking. Matching that behaviour keeps a link
// behaving the same way whichever provider claimed it.
//
// xterm.js only ever offers http/https here (OscLinkProvider drops other
// schemes unless allowNonHttpProtocols is set), so there is no protocol
// check to repeat. noopener is what stops the opened page reaching back
// through window.opener.
//
// This applies to the xterm backend only. ghostty-web has no such option
// and needs none: its own link providers call window.open with noopener
// directly, and only on a ctrl/cmd-click. The two backends differ in
// gesture, which is their own convention rather than something this file
// should paper over; what matters is that both open the whole URL and
// neither interrogates the user first.
export const linkHandler = {
  activate(_event: MouseEvent, uri: string) {
    window.open(uri, "_blank", "noopener,noreferrer");
  },
};

async function loadXterm(): Promise<TerminalBackend> {
  const [{ Terminal }, { FitAddon }, { WebLinksAddon }] = await Promise.all([
    import("@xterm/xterm"),
    import("@xterm/addon-fit"),
    import("@xterm/addon-web-links"),
  ]);
  // Only fetch xterm's CSS when xterm is actually selected; on the default
  // ghostty path this saves a stylesheet request.
  await import("@xterm/xterm/css/xterm.css");
  class PolicyTerminal extends Terminal {
    constructor(options?: ConstructorParameters<typeof Terminal>[0]) {
      super(options);
      if (!installXtermSelectionPolicy(this as XtermMouseTerminal)) {
        console.warn("[terminalRenderer] xterm selection policy not installed");
      }
    }
  }
  return {
    kind: "xterm" as const,
    Terminal: PolicyTerminal as unknown as TerminalCtor,
    FitAddon: FitAddon as unknown as FitAddonCtor,
    WebLinksAddon: WebLinksAddon as unknown as WebLinksAddonCtor,
  };
}
