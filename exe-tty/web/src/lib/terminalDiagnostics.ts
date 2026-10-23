// Terminal diagnostics: an opt-in, in-page event log for debugging the
// keyboard/composition/latency interaction in the web terminal.
//
// Terminal.vue (and any other page that embeds a terminal) creates one of
// these stores and hands it to a <TerminalDiagnostics> pane. When enabled it records, in order:
//   - every keydown (key, code, modifiers, isComposing)
//   - compositionstart / compositionupdate / compositionend
//   - each onData emission from the renderer (the bytes headed to the PTY)
//   - each chunk of output coming back, with a measured round-trip time
// so the exact interleaving of macOS composition events, keydowns, and PTY
// latency is directly observable without devtools.
//
// Logging is gated on `enabled`; when off, the hot-path helpers return
// immediately and DOM listeners stay cheap (they early-out before allocating).

import { ref, type Ref } from "vue";

export type DiagKind = "keydown" | "composition" | "ondata" | "input" | "output" | "info";

export interface DiagEntry {
  id: number;
  // Milliseconds since the store was created.
  t: number;
  kind: DiagKind;
  msg: string;
  // Optional structured detail, already stringified for display.
  detail?: string;
}

// Cap the retained log so a long-running session can't grow unbounded.
const MAX_ENTRIES = 2000;

export interface TerminalDiagnostics {
  enabled: Ref<boolean>;
  entries: Ref<DiagEntry[]>;
  // DOM instrumentation: attach keydown/composition listeners to the element
  // the renderer binds input to. Returns a disposer. Safe to call when
  // disabled (it still attaches, but the handlers early-out).
  instrument(el: HTMLElement | null | undefined): () => void;
  // Renderer -> PTY: a chunk the terminal emitted via onData.
  logOnData(data: string): void;
  // We sent a chunk to the websocket; record the time so the next output can
  // report a round-trip. Pass the bytes for display.
  logInputSent(data: string): void;
  // Bytes arrived back from the PTY. Reports an RTT against the oldest
  // outstanding input, if any.
  logOutput(byteLength: number): void;
  clear(): void;
}

function previewString(s: string): string {
  // Make control characters visible and keep the line short.
  const shown = s
    .replace(/\x1b/g, "\u241b") // ESC -> ␛
    .replace(/\r/g, "\u240d")
    .replace(/\n/g, "\u240a")
    .replace(/\t/g, "\u2409")
    .replace(/ /g, "\u2423");
  return shown.length > 60 ? shown.slice(0, 60) + "\u2026" : shown;
}

export function createTerminalDiagnostics(): TerminalDiagnostics {
  const enabled = ref(false);
  const entries = ref<DiagEntry[]>([]);
  const startedAt = (typeof performance !== "undefined" ? performance : Date).now();
  let nextId = 1;
  // FIFO of outstanding input send timestamps, to estimate echo round-trips.
  const pendingEcho: number[] = [];

  function now(): number {
    return (typeof performance !== "undefined" ? performance : Date).now();
  }

  function add(kind: DiagKind, msg: string, detail?: string): void {
    if (!enabled.value) return;
    const list = entries.value;
    list.push({ id: nextId++, t: Math.round(now() - startedAt), kind, msg, detail });
    if (list.length > MAX_ENTRIES) list.splice(0, list.length - MAX_ENTRIES);
  }

  function instrument(el: HTMLElement | null | undefined): () => void {
    if (!el || typeof el.addEventListener !== "function")
      return () => {
        /* no-op */
      };
    const onKeyDown = (e: Event) => {
      const ev = e as KeyboardEvent;
      const mods = [
        ev.ctrlKey ? "ctrl" : "",
        ev.altKey ? "alt" : "",
        ev.metaKey ? "meta" : "",
        ev.shiftKey ? "shift" : "",
      ]
        .filter(Boolean)
        .join("+");
      add(
        "keydown",
        ev.key === " " ? "\u2423" : ev.key,
        `code=${ev.code} keyCode=${ev.keyCode} isComposing=${ev.isComposing}${mods ? " " + mods : ""}`,
      );
    };
    const onCompStart = (e: Event) =>
      add("composition", "start", `data=${JSON.stringify((e as CompositionEvent).data)}`);
    const onCompUpdate = (e: Event) =>
      add("composition", "update", `data=${JSON.stringify((e as CompositionEvent).data)}`);
    const onCompEnd = (e: Event) =>
      add("composition", "end", `data=${JSON.stringify((e as CompositionEvent).data)}`);
    // Capture phase so we observe events before the renderer consumes/stops them.
    el.addEventListener("keydown", onKeyDown, true);
    el.addEventListener("compositionstart", onCompStart, true);
    el.addEventListener("compositionupdate", onCompUpdate, true);
    el.addEventListener("compositionend", onCompEnd, true);
    return () => {
      el.removeEventListener("keydown", onKeyDown, true);
      el.removeEventListener("compositionstart", onCompStart, true);
      el.removeEventListener("compositionupdate", onCompUpdate, true);
      el.removeEventListener("compositionend", onCompEnd, true);
    };
  }

  function logOnData(data: string): void {
    add("ondata", previewString(data), `${data.length} char${data.length === 1 ? "" : "s"}`);
  }

  function logInputSent(data: string): void {
    if (!enabled.value) return;
    pendingEcho.push(now());
    if (pendingEcho.length > 256) pendingEcho.shift();
    add("input", previewString(data), `${data.length}B → ws`);
  }

  function logOutput(byteLength: number): void {
    if (!enabled.value) return;
    const sent = pendingEcho.shift();
    const rtt = sent !== undefined ? `  rtt=${Math.round(now() - sent)}ms` : "";
    add("output", `pty→ ${byteLength}B${rtt}`);
  }

  function clear(): void {
    entries.value = [];
    pendingEcho.length = 0;
  }

  return { enabled, entries, instrument, logOnData, logInputSent, logOutput, clear };
}
