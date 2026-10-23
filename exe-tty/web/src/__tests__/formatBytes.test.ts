// formatBytes is shared by several views (support attachments, the terminal's
// upload queue, disk sizes), which is why it is worth pinning: it used to
// exist twice with different rounding.
import { describe, it, expect } from "vitest";
import { formatBytes } from "../lib/formatBytes";

describe("formatBytes", () => {
  it("scales through the units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1024)).toBe("1 KB");
    expect(formatBytes(409_600)).toBe("400 KB");
    expect(formatBytes(1024 * 1024)).toBe("1 MB");
    expect(formatBytes(1024 ** 3)).toBe("1 GB");
    expect(formatBytes(1024 ** 4)).toBe("1 TB");
    // Past the largest unit it keeps scaling the number, not the unit.
    expect(formatBytes(1024 ** 5)).toBe("1024 TB");
  });

  it("keeps a decimal only where it carries information", () => {
    // Below 10 of a unit a tenth is meaningful; above it, it is noise.
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(43_500)).toBe("42 KB");
  });

  it("does not render garbage for degenerate input", () => {
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(NaN)).toBe("0 B");
    expect(formatBytes(Infinity)).toBe("0 B");
  });
});
