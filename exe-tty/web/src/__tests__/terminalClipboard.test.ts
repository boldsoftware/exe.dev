import { describe, expect, it } from "vitest";
import {
  beginDeferredClipboardWrite,
  isPlainOsc52CopyKey,
  isTerminalCopyShortcut,
  Osc52ClipboardParser,
  TerminalCopyHintDetector,
} from "../lib/terminalClipboard";

const encode = (text: string) => {
  let binary = "";
  for (const byte of new TextEncoder().encode(text)) binary += String.fromCharCode(byte);
  return btoa(binary);
};
const bytes = (text: string) => new TextEncoder().encode(text);

describe("terminal clipboard keyboard shortcuts", () => {
  const keyEvent = (
    overrides: Partial<
      Pick<KeyboardEvent, "key" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">
    > = {},
  ) => ({
    key: "c",
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    shiftKey: false,
    ...overrides,
  });

  it("uses the logical key for non-QWERTY copy shortcuts", () => {
    expect(isTerminalCopyShortcut(keyEvent({ ctrlKey: true }))).toBe(true);
  });

  it("arms OSC 52 only for an unmodified logical lowercase c", () => {
    expect(isPlainOsc52CopyKey(keyEvent(), false, false)).toBe(true);
    expect(isPlainOsc52CopyKey(keyEvent({ key: "j" }), false, false)).toBe(false);
    expect(isPlainOsc52CopyKey(keyEvent({ ctrlKey: true }), false, false)).toBe(false);
    expect(isPlainOsc52CopyKey(keyEvent(), true, false)).toBe(false);
  });

  it("arms OSC 52 for CapsLock C but not Shift-C", () => {
    expect(isPlainOsc52CopyKey(keyEvent({ key: "C" }), false, false)).toBe(true);
    expect(isPlainOsc52CopyKey(keyEvent({ key: "C", shiftKey: true }), false, false)).toBe(false);
  });
});

describe("terminal copy hints", () => {
  it("recognizes plain, styled, quoted and wrapped c-to-copy affordances", () => {
    const cases = [
      "Browser did not open? (c to copy)\r\n",
      "Couldn't open a browser — press \x1b[1mc\x1b[0m to copy the URL",
      "press 'c' to copy",
      "press c to\r\ncopy the URL",
      "press c  to  copy",
    ];
    for (const text of cases) {
      const detector = new TerminalCopyHintDetector();
      expect(detector.push(bytes(text)), text).toBe(true);
    }
  });

  it("recognizes the affordance across websocket chunks", () => {
    const detector = new TerminalCopyHintDetector();
    expect(detector.push(bytes("Browser did not open? (c to co"))).toBe(false);
    expect(detector.push(bytes("py)\r\n"))).toBe(true);
  });

  it("does not arm for hidden OSC or opaque-string content", () => {
    const hidden = [
      `\x1b]0;c to copy\x07`,
      `\x1bPhidden c to copy\x1b\\`,
      `\x1bXhidden c to copy\x1b\\`,
      `\x1b^hidden c to copy\x1b\\`,
      `\x1b_hidden c to copy\x1b\\`,
    ];
    for (const text of hidden) {
      const detector = new TerminalCopyHintDetector();
      expect(detector.push(bytes(text)), text).toBe(false);
    }
  });

  it("does not arm for ordinary c output or a Command-C affordance", () => {
    const detector = new TerminalCopyHintDetector();
    expect(detector.push(bytes("cat README and continue\r\n"))).toBe(false);
    expect(detector.push(bytes("Press ⌘C to copy its markup\r\n"))).toBe(false);
  });
});

describe("deferred terminal clipboard writes", () => {
  class FakeClipboardItem {
    constructor(readonly data: Record<string, Blob | PromiseLike<Blob>>) {}
    async getType(type: string): Promise<Blob> {
      return this.data[type];
    }
  }

  it("starts the browser write before the OSC 52 payload is available", async () => {
    let calls = 0;
    let copied = "";
    const clipboard = {
      async write(items: FakeClipboardItem[]) {
        calls++;
        copied = await (await items[0].getType("text/plain")).text();
      },
    };

    const pending = beginDeferredClipboardWrite(
      clipboard as unknown as Clipboard,
      FakeClipboardItem as unknown as typeof ClipboardItem,
    );
    expect(pending).not.toBeNull();
    expect(calls).toBe(1);
    expect(copied).toBe("");

    expect(await pending!.resolve("exact URL")).toBe(true);
    expect(copied).toBe("exact URL");
  });

  it("cancels without writing stale text", async () => {
    let copied = "";
    const clipboard = {
      async write(items: FakeClipboardItem[]) {
        copied = await (await items[0].getType("text/plain")).text();
      },
    };

    const pending = beginDeferredClipboardWrite(
      clipboard as unknown as Clipboard,
      FakeClipboardItem as unknown as typeof ClipboardItem,
    )!;
    pending.cancel();

    expect(await pending.resolve("too late")).toBe(false);
    expect(copied).toBe("");
  });
});

describe("Osc52ClipboardParser", () => {
  it("decodes the exact OSC 52 clipboard payload across websocket chunks", () => {
    const parser = new Osc52ClipboardParser();
    const url =
      "https://claude.com/cai/oauth/authorize?redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback";
    const sequence = `\x1b]52;c;${encode(url)}\x07`;

    const copied = [
      ...parser.push(bytes(sequence.slice(0, 4))),
      ...parser.push(bytes(sequence.slice(4, 19))),
      ...parser.push(bytes(sequence.slice(19))),
    ];

    expect(copied).toEqual([url]);
  });

  it("supports the string terminator and multiple writes in one chunk", () => {
    const parser = new Osc52ClipboardParser();
    const input = `\x1b]52;c;${encode("first")}\x1b\\noise\x1b]52;;${encode("second")}\x07`;

    expect(parser.push(bytes(input))).toEqual(["first", "second"]);
  });

  it("ignores non-clipboard selection targets", () => {
    const parser = new Osc52ClipboardParser();
    expect(parser.push(bytes(`\x1b]52;p;${encode("primary")}\x07`))).toEqual([]);
  });

  it("does not extract OSC 52 hidden inside an opaque terminal string", () => {
    const parser = new Osc52ClipboardParser();
    for (const introducer of ["P", "X", "^", "_"]) {
      parser.reset();
      const nested = `\x1b${introducer}opaque\x1b]52;c;${encode("smuggled")}\x07\x1b\\`;
      expect(parser.push(bytes(nested)), introducer).toEqual([]);
    }
  });

  it("aborts an OSC on CAN or SUB and accepts a later valid write", () => {
    const parser = new Osc52ClipboardParser();
    for (const abort of ["\x18", "\x1a"]) {
      parser.reset();
      const input = `\x1b]52;c;${encode("bad")}${abort}\x1b]52;c;${encode("good")}\x07`;
      expect(parser.push(bytes(input)), abort.charCodeAt(0).toString(16)).toEqual(["good"]);
    }
  });

  it("recovers when a new OSC aborts a truncated one", () => {
    const parser = new Osc52ClipboardParser();
    const input = `\x1b]0;unterminated\x1b]52;c;${encode("recovered")}\x07`;
    expect(parser.push(bytes(input))).toEqual(["recovered"]);
  });

  it("decodes UTF-8 clipboard text", () => {
    const parser = new Osc52ClipboardParser();
    const text = "curly quotes “work” and emoji 💝";

    expect(parser.push(bytes(`\x1b]52;c;${encode(text)}\x07`))).toEqual([text]);
  });

  it("does not mistake UTF-8 continuation bytes for C1 OSC controls", () => {
    const parser = new Osc52ClipboardParser();
    const prefix = bytes("ordinary output: ” 💝 ├\r\n");
    const sequence = bytes(`\x1b]52;c;${encode("still works")}\x07`);

    expect(parser.push(prefix)).toEqual([]);
    expect(parser.push(sequence)).toEqual(["still works"]);
  });

  it("accepts the default empty selection parameter", () => {
    const parser = new Osc52ClipboardParser();

    expect(parser.push(bytes(`\x1b]52;;${encode("default selection")}\x07`))).toEqual([
      "default selection",
    ]);
  });

  it("ignores clipboard queries", () => {
    const parser = new Osc52ClipboardParser();
    expect(parser.push(bytes("\x1b]52;c;?\x07"))).toEqual([]);
  });

  it("ignores malformed base64", () => {
    const parser = new Osc52ClipboardParser();
    expect(parser.push(bytes("\x1b]52;c;not*base64\x07"))).toEqual([]);
  });

  it("ignores oversized payloads", () => {
    const parser = new Osc52ClipboardParser(4);
    expect(parser.push(bytes(`\x1b]52;c;${encode("12345")}\x07`))).toEqual([]);
  });

  it("drops an incomplete sequence when reset between terminal sessions", () => {
    const parser = new Osc52ClipboardParser();
    parser.push(bytes(`\x1b]52;c;${encode("old")}`));
    parser.reset();

    expect(parser.push(bytes("\x07"))).toEqual([]);
    expect(parser.push(bytes(`\x1b]52;c;${encode("new")}\x07`))).toEqual(["new"]);
  });
});
