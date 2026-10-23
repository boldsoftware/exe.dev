// Window titles that programs set with OSC 0/2 (shell prompts, Claude Code,
// vim, ...), used to label the terminal's session tabs the way the iOS app
// does. The emulators would tell us too, but not uniformly: ghostty-web only
// looks for titles in string writes (we write bytes) and only within a single
// write, so a title split across websocket frames is lost. Parsing the byte
// stream ourselves works the same for both renderers.

const ESC = 0x1b;
const BEL = 0x07;
const CAN = 0x18;
const SUB = 0x1a;
const SEMICOLON = 0x3b;
const RIGHT_BRACKET = 0x5d;

// Titles are cut at this many bytes; nobody reads a 1 KB tab label, and the
// cap keeps a hostile stream from growing the buffer.
const MAX_TITLE_BYTES = 1024;

const enum State {
  Ground,
  Escape,
  OscCommand,
  OscTitle,
  OscDiscard,
}

/**
 * Extracts window titles (OSC 0 and OSC 2) from terminal output.
 *
 * Streaming, because output arrives in independently framed websocket
 * messages and any part of ESC ] 2 ; title BEL may be split across push()
 * calls. BEL ends the string, and so does ESC (whether or not a backslash
 * follows to make ST), as in the DEC/xterm parser model; CAN/SUB abort it.
 * The caller still hands every byte to the emulator; this only observes.
 * Not handled: the XTWINOPS title stack (CSI 22/23 t) and 8-bit C1 controls.
 */
export class TerminalTitleParser {
  private state = State.Ground;
  private command = "";
  private title = new Uint8Array(MAX_TITLE_BYTES);
  private titleLength = 0;
  private truncated = false;
  private readonly decoder = new TextDecoder();

  /** Returns the titles completed by `data`, oldest first ("" = cleared). */
  push(data: Uint8Array): string[] {
    const titles: string[] = [];
    for (let index = 0; index < data.length; index++) {
      // Most output has no escapes, and an OSC we don't care about (a
      // clipboard write, an inline image) can be huge; let the native search
      // skip those in bulk.
      if (this.state === State.Ground || this.state === State.OscDiscard) {
        const escapeAt = data.indexOf(ESC, index);
        if (this.state === State.Ground) {
          if (escapeAt === -1) break;
          index = escapeAt;
        } else {
          const belAt = data.indexOf(BEL, index);
          const end = belAt === -1 || (escapeAt !== -1 && escapeAt < belAt) ? escapeAt : belAt;
          if (end === -1) break;
          index = end;
          if (end === belAt) {
            this.state = State.Ground;
            continue;
          }
        }
        this.state = State.Escape;
        continue;
      }
      const byte = data[index];
      if (byte === CAN || byte === SUB) {
        this.state = State.Ground;
        continue;
      }
      switch (this.state) {
        case State.Escape:
          if (byte === RIGHT_BRACKET) {
            this.command = "";
            this.state = State.OscCommand;
          } else if (byte !== ESC) {
            // Anything else, including the start of a DCS/APC/... string, is
            // ignorable: an ESC ends those too, so an OSC can't hide in one.
            this.state = State.Ground;
          }
          break;

        case State.OscCommand:
          if (byte >= 0x30 && byte <= 0x39 && this.command.length < 4) {
            this.command += String.fromCharCode(byte);
          } else if (!this.isTitleCommand()) {
            this.discard(byte);
          } else if (byte === SEMICOLON) {
            this.titleLength = 0;
            this.truncated = false;
            this.state = State.OscTitle;
          } else if (byte === BEL || byte === ESC) {
            // `ESC ] 2 BEL`: no payload, which xterm takes as clearing it.
            titles.push("");
            this.state = byte === ESC ? State.Escape : State.Ground;
          } else {
            this.discard(byte);
          }
          break;

        case State.OscTitle:
          if (byte === BEL || byte === ESC) {
            titles.push(this.finishTitle());
            this.state = byte === ESC ? State.Escape : State.Ground;
          } else if (this.titleLength < MAX_TITLE_BYTES) {
            this.title[this.titleLength++] = byte;
          } else {
            this.truncated = true;
          }
          break;
      }
    }
    return titles;
  }

  /** Forget any partial sequence, e.g. when a new connection starts. */
  reset() {
    this.state = State.Ground;
  }

  // OSC 0 (icon name and title) or OSC 2 (title); "02" counts, as in xterm.
  private isTitleCommand(): boolean {
    if (this.command === "") return false;
    const n = Number(this.command);
    return n === 0 || n === 2;
  }

  private finishTitle(): string {
    const text = this.decoder.decode(this.title.subarray(0, this.titleLength));
    // A cut can land inside a UTF-8 sequence; drop the replacement it decodes to.
    return this.truncated ? text.replace(/\uFFFD$/, "") : text;
  }

  // Skip the rest of an OSC we don't care about.
  private discard(byte: number) {
    if (byte === BEL) this.state = State.Ground;
    else if (byte === ESC) this.state = State.Escape;
    else this.state = State.OscDiscard;
  }
}

// Controls and invisible format characters (including the bidi overrides,
// which would reorder the text around a title), except the joiners emoji and
// some scripts need, and tag characters inside a flag sequence.
const UNPRINTABLE =
  /(?![\u200c\u200d\u{e0020}-\u{e007f}])[\p{Cc}\p{Cf}]|(?<!\u{1f3f4}[\u{e0020}-\u{e007e}]*)[\u{e0020}-\u{e007f}]/gu;
const PRINTABLE = /[^\s\u200c\u200d\u{e0020}-\u{e007f}]/u;

/**
 * Normalize a title for display: drop control and format characters and
 * surrounding whitespace. Returns null when nothing visible is left (the
 * program cleared it).
 */
export function cleanTerminalTitle(raw: string): string | null {
  const cleaned = raw.replace(UNPRINTABLE, "").trim();
  return PRINTABLE.test(cleaned) ? cleaned : null;
}

const segmenter =
  typeof Intl !== "undefined" && "Segmenter" in Intl
    ? new Intl.Segmenter(undefined, { granularity: "grapheme" })
    : null;

// Characters a monospace font draws two cells wide: East Asian wide and
// fullwidth forms, and emoji.
const WIDE =
  /[\u1100-\u115f\u2e80-\u303e\u3041-\u33ff\u3400-\u4dbf\u4e00-\u9fff\ua000-\ua4cf\uac00-\ud7a3\uf900-\ufaff\ufe30-\ufe4f\uff00-\uff60\uffe0-\uffe6\u{20000}-\u{3fffd}\ufe0f]|\p{Emoji_Presentation}/u;
// Right-to-left scripts (Hebrew, Arabic, Syriac, Thaana, N'Ko, ...).
const RTL = /[\u0590-\u08ff\ufb1d-\ufdff\ufe70-\ufefc]/u;

/**
 * Split off the last `columns` cells of `text`, without breaking a character.
 * Tab labels render the head with a CSS ellipsis and the tail whole, which
 * elides the middle of a long title at whatever width the tab has: the
 * distinguishing part is often at either end (`user@vm: ~/src/app`, `✳ Fix
 * the flaky test`). Right-to-left text isn't split (each piece would get its
 * own bidi ordering), so it's cut at the end instead.
 */
export function splitTitleTail(text: string, columns: number): { head: string; tail: string } {
  if (RTL.test(text)) return { head: text, tail: "" };
  const chars = segmenter
    ? Array.from(segmenter.segment(text), (s) => s.segment)
    : Array.from(text);
  let start = chars.length;
  for (let used = 0; start > 0; start--) {
    used += WIDE.test(chars[start - 1]) ? 2 : 1;
    if (used > columns) break;
  }
  return { head: chars.slice(0, start).join(""), tail: chars.slice(start).join("") };
}

// How long a remembered title for a session the server doesn't list is kept.
// Another browser tab can have just created that session, after the listing
// we're looking at was taken.
const PRUNE_GRACE_MS = 60_000;

/**
 * Last-seen titles per session, kept in localStorage so tabs stay labelled
 * across reloads and for sessions this page isn't attached to (only the
 * attached session's output reaches us, and reattaching doesn't replay the
 * title). One key per session, so two browser tabs on the same host never
 * overwrite each other's entries.
 */
export class SessionTitleStore {
  constructor(private readonly prefix: string) {}

  load(): Map<string, string> {
    const out = new Map<string, string>();
    for (const [name, entry] of this.entries()) out.set(name, entry.title);
    return out;
  }

  set(name: string, title: string | null) {
    try {
      if (title === null) localStorage.removeItem(this.prefix + name);
      else localStorage.setItem(this.prefix + name, JSON.stringify({ title, at: Date.now() }));
    } catch {
      /* ignore quota / disabled storage */
    }
  }

  /**
   * Forget sessions not in `alive`, except recently titled ones (see
   * PRUNE_GRACE_MS). Returns the names forgotten.
   */
  retain(alive: Set<string>, now = Date.now()): string[] {
    const gone: string[] = [];
    for (const [name, entry] of this.entries()) {
      if (!alive.has(name) && now - entry.at > PRUNE_GRACE_MS) gone.push(name);
    }
    for (const name of gone) this.set(name, null);
    return gone;
  }

  /**
   * Interpret a `storage` event (another browser tab on this host changed a
   * title): the session it's about and its new title, or null if the event
   * isn't ours. A null key means storage was cleared.
   */
  changeFrom(
    ev: Pick<StorageEvent, "key" | "newValue">,
  ): { name: string; title: string | null } | { cleared: true } | null {
    if (ev.key === null) return { cleared: true };
    if (!ev.key.startsWith(this.prefix)) return null;
    return { name: ev.key.slice(this.prefix.length), title: decode(ev.newValue)?.title ?? null };
  }

  private entries(): Array<[string, { title: string; at: number }]> {
    const out: Array<[string, { title: string; at: number }]> = [];
    try {
      for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i);
        if (!key?.startsWith(this.prefix)) continue;
        const entry = decode(localStorage.getItem(key));
        if (entry) out.push([key.slice(this.prefix.length), entry]);
      }
    } catch {
      /* storage unavailable */
    }
    return out;
  }
}

function decode(raw: string | null): { title: string; at: number } | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw);
    if (typeof v?.title === "string" && v.title !== "" && typeof v.at === "number") return v;
  } catch {
    /* not ours */
  }
  return null;
}
