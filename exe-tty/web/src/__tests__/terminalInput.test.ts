import { describe, expect, it } from "vitest";
import { MAX_INPUT, splitInput } from "../lib/terminalInput";

describe("splitInput", () => {
  it("sends short input as is", () => {
    expect(splitInput("ls\r")).toEqual(["ls\r"]);
  });

  it("splits a long paste into pieces a server will take", () => {
    const paste = "\u0001".repeat(MAX_INPUT * 2 + 1);
    const pieces = splitInput(paste);
    expect(pieces.join("")).toBe(paste);
    for (const p of pieces) {
      // Each piece fits in 32 KiB (API.md) even with every character escaped.
      expect(
        new TextEncoder().encode(JSON.stringify({ type: "input", data: p })).length,
      ).toBeLessThanOrEqual(32 << 10);
    }
  });

  it("never splits a character", () => {
    const paste = "a" + "😀".repeat(MAX_INPUT);
    const pieces = splitInput(paste);
    expect(pieces.join("")).toBe(paste);
    for (const p of pieces) {
      expect(p.isWellFormed()).toBe(true);
    }
  });
});
