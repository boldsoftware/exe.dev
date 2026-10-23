type ClipboardKeyEvent = Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">;

export function isPlainOsc52CopyKey(
  event: ClipboardKeyEvent,
  stickyCtrl: boolean,
  stickyAlt: boolean,
): boolean {
  return (
    event.key.toLowerCase() === "c" &&
    !event.ctrlKey &&
    !event.metaKey &&
    !event.altKey &&
    !event.shiftKey &&
    !stickyCtrl &&
    !stickyAlt
  );
}

export function isTerminalCopyShortcut(event: ClipboardKeyEvent): boolean {
  return event.key.toLowerCase() === "c" && !event.altKey && (event.ctrlKey || event.metaKey);
}

const enum HintState {
  Ground,
  Escape,
  CSI,
  OSC,
  OSCEscape,
  OpaqueString,
  OpaqueStringEscape,
}

export class TerminalCopyHintDetector {
  private decoder = new TextDecoder();
  private visibleTail = "";
  private state = HintState.Ground;

  constructor(private readonly tailLength = 1024) {}

  push(data: Uint8Array): boolean {
    const visible = new Uint8Array(data.length);
    let visibleLength = 0;

    for (const byte of data) {
      if (byte === 0x18 || byte === 0x1a) {
        this.state = HintState.Ground;
        continue;
      }
      switch (this.state) {
        case HintState.Ground:
          if (byte === 0x1b) this.state = HintState.Escape;
          else visible[visibleLength++] = byte;
          break;
        case HintState.Escape:
          if (byte === 0x5b) this.state = HintState.CSI;
          else if (byte === 0x5d) this.state = HintState.OSC;
          else if (byte === 0x50 || byte === 0x58 || byte === 0x5e || byte === 0x5f) {
            this.state = HintState.OpaqueString;
          } else if (byte !== 0x1b && !(byte >= 0x20 && byte <= 0x2f)) {
            this.state = HintState.Ground;
          }
          break;
        case HintState.CSI:
          if (byte === 0x1b) this.state = HintState.Escape;
          else if (byte >= 0x40 && byte <= 0x7e) this.state = HintState.Ground;
          break;
        case HintState.OSC:
          if (byte === 0x07) this.state = HintState.Ground;
          else if (byte === 0x1b) this.state = HintState.OSCEscape;
          break;
        case HintState.OSCEscape:
          if (byte === 0x5c) this.state = HintState.Ground;
          else this.state = byte === 0x1b ? HintState.OSCEscape : HintState.OSC;
          break;
        case HintState.OpaqueString:
          if (byte === 0x1b) this.state = HintState.OpaqueStringEscape;
          break;
        case HintState.OpaqueStringEscape:
          if (byte === 0x5c) this.state = HintState.Ground;
          else this.state = byte === 0x1b ? HintState.OpaqueStringEscape : HintState.OpaqueString;
          break;
      }
    }

    const text = this.decoder.decode(visible.subarray(0, visibleLength), { stream: true });
    const combined = this.visibleTail + text;
    const normalized = combined
      .replace(/['"`]/g, "")
      .replace(/[\t\r\n ]+/g, " ")
      .toLowerCase();
    this.visibleTail = combined.slice(-this.tailLength);
    if (!/(?:^|[\s(])c\s+to\s+copy\b/.test(normalized)) return false;
    this.visibleTail = "";
    return true;
  }

  reset() {
    this.decoder = new TextDecoder();
    this.visibleTail = "";
    this.state = HintState.Ground;
  }
}

export interface DeferredClipboardWrite {
  resolve(text: string): Promise<boolean>;
  cancel(): void;
}

type ClipboardWriter = {
  write(items: ClipboardItem[]): Promise<void>;
};

/**
 * Starts a clipboard write while the keydown still has browser user activation,
 * but defers supplying the text until the terminal answers with OSC 52.
 *
 * Safari requires navigator.clipboard.write() to be entered directly from a
 * user gesture. The terminal response arrives later over WebSocket, so calling
 * writeText() at response time is too late there. ClipboardItem permits a
 * promised representation: begin the write on keydown, then resolve that
 * promise with the OSC 52 payload.
 */
export function beginDeferredClipboardWrite(
  clipboard: ClipboardWriter | undefined = navigator.clipboard,
  ClipboardItemClass: typeof ClipboardItem | undefined = globalThis.ClipboardItem,
): DeferredClipboardWrite | null {
  if (!clipboard?.write || !ClipboardItemClass) return null;

  let settled = false;
  let resolvePayload!: (blob: Blob) => void;
  let rejectPayload!: (reason?: unknown) => void;
  const payload = new Promise<Blob>((resolve, reject) => {
    resolvePayload = resolve;
    rejectPayload = reject;
  });
  // Some implementations may reject clipboard.write() before inspecting the
  // representation. Keep cancellation from becoming an unhandled rejection.
  void payload.catch(() => undefined);

  let writeResult: Promise<boolean>;
  try {
    const item = new ClipboardItemClass({ "text/plain": payload });
    writeResult = Promise.resolve(clipboard.write([item])).then(
      () => true,
      () => false,
    );
  } catch {
    rejectPayload(new DOMException("Clipboard write could not start", "AbortError"));
    return null;
  }

  return {
    resolve(text: string): Promise<boolean> {
      if (!settled) {
        settled = true;
        resolvePayload(new Blob([text], { type: "text/plain" }));
      }
      return writeResult;
    },
    cancel() {
      if (settled) return;
      settled = true;
      rejectPayload(new DOMException("Clipboard write cancelled", "AbortError"));
    },
  };
}

const ESC = 0x1b;
const BEL = 0x07;
const CAN = 0x18;
const SUB = 0x1a;
const SEMICOLON = 0x3b;
const BACKSLASH = 0x5c;

const enum State {
  Ground,
  Escape,
  OscCommand,
  OscSelection,
  OscData,
  OscDataEscape,
  OscDiscard,
  OscDiscardEscape,
  IgnoredString,
  IgnoredStringEscape,
}

/**
 * Extracts clipboard writes from OSC 52 terminal output.
 *
 * Terminal output arrives in independently-framed websocket messages, so the
 * parser is deliberately streaming: any part of ESC ] 52 ; Pc ; Pd ST may be
 * split across calls to push(). The escape sequence is still passed to the
 * emulator by the caller; this class only observes it.
 */
export class Osc52ClipboardParser {
  private state = State.Ground;
  private command = "";
  private selection = "";
  private payload = "";
  private readonly maxEncodedLength: number;

  constructor(private readonly maxPayloadBytes = 1024 * 1024) {
    this.maxEncodedLength = Math.ceil((maxPayloadBytes * 4) / 3) + 4;
  }

  push(data: Uint8Array): string[] {
    const copied: string[] = [];

    for (let index = 0; index < data.length; index++) {
      // Most terminal output contains no escapes. Let the typed array's native
      // search skip ordinary UTF-8 in bulk instead of running every byte
      // through the state-machine switch on the main thread.
      if (this.state === State.Ground) {
        const escapeAt = data.indexOf(ESC, index);
        if (escapeAt === -1) break;
        this.state = State.Escape;
        index = escapeAt;
        continue;
      }

      const byte = data[index];
      if (byte === CAN || byte === SUB) {
        this.reset();
        continue;
      }
      switch (this.state) {
        case State.Escape:
          if (byte === 0x5d) this.startOsc();
          else if (byte === 0x50 || byte === 0x58 || byte === 0x5e || byte === 0x5f) {
            // DCS, SOS, PM and APC are opaque strings terminated by ST. An
            // OSC-looking byte sequence inside one is data, not a clipboard
            // instruction.
            this.state = State.IgnoredString;
          } else this.state = byte === ESC ? State.Escape : State.Ground;
          break;

        case State.OscCommand:
          if (this.isTerminator(byte)) {
            this.reset();
          } else if (byte === SEMICOLON) {
            this.state = this.command === "52" ? State.OscSelection : State.OscDiscard;
          } else if (byte >= 0x30 && byte <= 0x39 && this.command.length < 4) {
            this.command += String.fromCharCode(byte);
          } else {
            this.state = byte === ESC ? State.OscDiscardEscape : State.OscDiscard;
          }
          break;

        case State.OscSelection:
          if (this.isTerminator(byte)) {
            this.reset();
          } else if (byte === SEMICOLON) {
            this.state = State.OscData;
          } else if (byte === ESC) {
            this.state = State.OscDiscardEscape;
          } else if (this.selection.length < 16) {
            this.selection += String.fromCharCode(byte);
          } else {
            this.state = State.OscDiscard;
          }
          break;

        case State.OscData:
          if (byte === BEL) {
            this.finish(copied);
          } else if (byte === ESC) {
            this.state = State.OscDataEscape;
          } else if (this.payload.length < this.maxEncodedLength) {
            this.payload += String.fromCharCode(byte);
          } else {
            this.state = State.OscDiscard;
          }
          break;

        case State.OscDataEscape:
          if (byte === BACKSLASH) {
            this.finish(copied);
          } else if (byte === 0x5d) {
            // A fresh OSC aborts a truncated/malformed one. This lets a later
            // valid clipboard request recover without waiting for BEL/ST.
            this.startOsc();
          } else {
            this.state = State.OscDiscard;
          }
          break;

        case State.OscDiscard:
          if (byte === BEL) this.reset();
          else if (byte === ESC) this.state = State.OscDiscardEscape;
          break;

        case State.OscDiscardEscape:
          if (byte === BACKSLASH) this.reset();
          else if (byte === 0x5d) this.startOsc();
          else this.state = byte === ESC ? State.OscDiscardEscape : State.OscDiscard;
          break;

        case State.IgnoredString:
          if (byte === ESC) this.state = State.IgnoredStringEscape;
          break;

        case State.IgnoredStringEscape:
          if (byte === BACKSLASH) this.reset();
          else this.state = byte === ESC ? State.IgnoredStringEscape : State.IgnoredString;
          break;
      }
    }

    return copied;
  }

  reset() {
    this.state = State.Ground;
    this.command = "";
    this.selection = "";
    this.payload = "";
  }

  private startOsc() {
    this.command = "";
    this.selection = "";
    this.payload = "";
    this.state = State.OscCommand;
  }

  private isTerminator(byte: number): boolean {
    return byte === BEL;
  }

  private finish(copied: string[]) {
    // Browser terminals have one clipboard, not the X11/DEC selection set.
    // Honor the standard clipboard target (c) and the default empty target;
    // do not silently map primary/secondary/cut-buffer writes onto it.
    const text = this.selection === "" || this.selection === "c" ? this.decodePayload() : null;
    if (text !== null) copied.push(text);
    this.reset();
  }

  private decodePayload(): string | null {
    if (!this.payload || this.payload === "?") return null;
    if (this.payload.length > this.maxEncodedLength || /[^A-Za-z0-9+/=]/.test(this.payload)) {
      return null;
    }

    const remainder = this.payload.length % 4;
    if (remainder === 1) return null;
    const padded =
      remainder === 0
        ? this.payload
        : this.payload.padEnd(this.payload.length + 4 - remainder, "=");

    try {
      const binary = atob(padded);
      if (binary.length > this.maxPayloadBytes) return null;
      const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
      return new TextDecoder().decode(bytes);
    } catch {
      return null;
    }
  }
}
