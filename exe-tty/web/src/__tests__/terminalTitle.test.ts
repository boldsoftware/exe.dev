import { beforeEach, describe, expect, it } from "vitest";
import {
  cleanTerminalTitle,
  SessionTitleStore,
  splitTitleTail,
  TerminalTitleParser,
} from "../lib/terminalTitle";

const bytes = (text: string) => new TextEncoder().encode(text);

describe("TerminalTitleParser", () => {
  it("reports OSC 2 and OSC 0 titles with either terminator", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;Claude Code\x07"))).toEqual(["Claude Code"]);
    expect(p.push(bytes("\x1b]0;✳ Fix the bug\x1b\\"))).toEqual(["✳ Fix the bug"]);
  });

  it("finds titles amid ordinary output and other escapes", () => {
    const p = new TerminalTitleParser();
    const out = "ls\r\n\x1b[1;32mfile\x1b[0m\r\n\x1b]0;user@vm: ~/src\x07$ ";
    expect(p.push(bytes(out))).toEqual(["user@vm: ~/src"]);
    expect(p.push(bytes("plain text, no escapes"))).toEqual([]);
  });

  it("reports every title in a chunk, in order", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;one\x07\x1b]2;two\x07"))).toEqual(["one", "two"]);
  });

  it("reassembles a title split across chunks, including mid-codepoint", () => {
    const p = new TerminalTitleParser();
    const all = bytes("\x1b]2;café ✳\x1b\\");
    const seen: string[] = [];
    for (const b of all) seen.push(...p.push(new Uint8Array([b])));
    expect(seen).toEqual(["café ✳"]);
  });

  it("reports a cleared title as empty", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;\x07"))).toEqual([""]);
  });

  it("ignores other OSCs and the icon-name-only OSC 1", () => {
    const p = new TerminalTitleParser();
    const out =
      "\x1b]1;icon\x07\x1b]52;c;aGk=\x07\x1b]8;;https://x\x1b\\link\x1b]8;;\x1b\\" +
      "\x1b]22;nope\x07\x1b];nope\x07";
    expect(p.push(bytes(out))).toEqual([]);
    // ...and is back in sync afterwards.
    expect(p.push(bytes("\x1b]2;ok\x07"))).toEqual(["ok"]);
  });

  it("skips a huge unrelated OSC split across chunks", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]52;c;" + "QUFB".repeat(50000)))).toEqual([]);
    expect(p.push(bytes("QUFB".repeat(50000) + "\x07after\x1b]2;next\x07"))).toEqual(["next"]);
  });

  it("reads the command number as xterm does", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]02;padded\x07"))).toEqual(["padded"]);
    // No payload at all clears the title.
    expect(p.push(bytes("\x1b]2\x07"))).toEqual([""]);
  });

  it("does not mistake the payload of a DCS/APC string for a title", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1bP]2;not a title\x07\x1b\\"))).toEqual([]);
    expect(p.push(bytes("\x1b_G]0;nor this\x07\x1b\\"))).toEqual([]);
  });

  it("ends the title at ESC like the emulators, and a fresh OSC can follow", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;first\x1b]2;second\x07"))).toEqual(["first", "second"]);
  });

  it("drops a title aborted by CAN/SUB", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;aborted\x18\x1b]2;kept\x07"))).toEqual(["kept"]);
  });

  it("cuts over-long titles without growing, on a character boundary", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;" + "x".repeat(5000) + "\x07"))).toEqual(["x".repeat(1024)]);
    // 1023 bytes of x, then a 3-byte character straddling the cut.
    expect(p.push(bytes("\x1b]2;" + "x".repeat(1023) + "✳✳\x07"))).toEqual(["x".repeat(1023)]);
    expect(p.push(bytes("\x1b]2;short\x07"))).toEqual(["short"]);
  });

  it("reset() forgets a partial sequence", () => {
    const p = new TerminalTitleParser();
    expect(p.push(bytes("\x1b]2;from the old socket"))).toEqual([]);
    p.reset();
    expect(p.push(bytes("not a title\x07"))).toEqual([]);
    expect(p.push(bytes("\x1b]2;new\x07"))).toEqual(["new"]);
  });
});

describe("cleanTerminalTitle", () => {
  it("strips control, format, and bidi characters and surrounding whitespace", () => {
    expect(cleanTerminalTitle("  user@vm:\t~/src\r\n")).toBe("user@vm:~/src");
    expect(cleanTerminalTitle("evil\u202ecod.exe")).toBe("evilcod.exe");
    expect(cleanTerminalTitle("\x7f\x9bo\u200bk\u00ad")).toBe("ok");
  });

  it("keeps emoji sequences intact", () => {
    expect(cleanTerminalTitle("👩‍💻 build")).toBe("👩‍💻 build");
    const england = "\u{1f3f4}\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}";
    expect(cleanTerminalTitle(england)).toBe(england);
    // ...but not tag characters outside a flag (invisible text).
    expect(cleanTerminalTitle("ok\u{e0068}\u{e0069}")).toBe("ok");
  });

  it("returns null when nothing visible is left", () => {
    expect(cleanTerminalTitle("")).toBeNull();
    expect(cleanTerminalTitle(" \x01 ")).toBeNull();
    expect(cleanTerminalTitle("\u200b\u200d")).toBeNull();
  });
});

describe("splitTitleTail", () => {
  it("is all tail when short", () => {
    expect(splitTitleTail("htop", 8)).toEqual({ head: "", tail: "htop" });
    expect(splitTitleTail("12345678", 8)).toEqual({ head: "", tail: "12345678" });
  });

  it("splits off the last characters", () => {
    expect(splitTitleTail("exedev@machine: ~/src/project", 8)).toEqual({
      head: "exedev@machine: ~/src",
      tail: "/project",
    });
  });

  it("counts wide characters as two cells and does not split graphemes", () => {
    expect(splitTitleTail("编译项目中请稍候完成测试", 8)).toEqual({
      head: "编译项目中请稍候",
      tail: "完成测试",
    });
    expect(splitTitleTail("a" + "👩‍💻".repeat(3), 4)).toEqual({
      head: "a👩‍💻",
      tail: "👩‍💻👩‍💻",
    });
    expect(splitTitleTail("ab编", 2)).toEqual({ head: "ab", tail: "编" });
    expect(splitTitleTail("a编", 1)).toEqual({ head: "a编", tail: "" });
  });

  it("does not split right-to-left text", () => {
    expect(splitTitleTail("vim שלום עולם.txt", 8)).toEqual({ head: "vim שלום עולם.txt", tail: "" });
  });
});

describe("SessionTitleStore", () => {
  beforeEach(() => localStorage.clear());

  it("round-trips titles per session and leaves other keys alone", () => {
    localStorage.setItem("xterm:h:tabOrder", "[]");
    const store = new SessionTitleStore("xterm:h:title:");
    store.set("amber", "user@vm: ~");
    store.set("constructor", "✳ Claude Code");
    expect(new SessionTitleStore("xterm:h:title:").load()).toEqual(
      new Map([
        ["amber", "user@vm: ~"],
        ["constructor", "✳ Claude Code"],
      ]),
    );
    // Another host's origin uses a different prefix.
    expect(new SessionTitleStore("xterm:other:title:").load().size).toBe(0);

    store.set("amber", null);
    expect([...store.load().keys()]).toEqual(["constructor"]);
    expect(localStorage.getItem("xterm:h:tabOrder")).toBe("[]");
  });

  it("prunes sessions that are gone, but not ones titled moments ago", () => {
    const store = new SessionTitleStore("p:");
    store.set("old", "a");
    store.set("alive", "b");
    const later = Date.now() + 120_000;
    store.set("fresh", "c"); // e.g. just created in another browser tab
    localStorage.setItem("p:fresh", JSON.stringify({ title: "c", at: later }));
    expect(store.retain(new Set(["alive"]), later)).toEqual(["old"]);
    expect([...store.load().keys()].sort()).toEqual(["alive", "fresh"]);
  });

  it("interprets storage events from other browser tabs", () => {
    const store = new SessionTitleStore("p:");
    const value = JSON.stringify({ title: "htop", at: 1 });
    expect(store.changeFrom({ key: "p:lime", newValue: value })).toEqual({
      name: "lime",
      title: "htop",
    });
    expect(store.changeFrom({ key: "p:lime", newValue: null })).toEqual({
      name: "lime",
      title: null,
    });
    expect(store.changeFrom({ key: "other", newValue: value })).toBeNull();
    expect(store.changeFrom({ key: null, newValue: null })).toEqual({ cleared: true });
  });
});
