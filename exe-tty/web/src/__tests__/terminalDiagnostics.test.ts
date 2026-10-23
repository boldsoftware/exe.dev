import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createTerminalDiagnostics } from "../lib/terminalDiagnostics";

describe("terminalDiagnostics", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("records nothing while disabled", () => {
    const d = createTerminalDiagnostics();
    d.logOnData("x");
    d.logInputSent("x");
    d.logOutput(3);
    expect(d.entries.value).toHaveLength(0);
  });

  it("records keydown/composition/onData/output once enabled", () => {
    const d = createTerminalDiagnostics();
    d.enabled.value = true;
    const el = document.createElement("div");
    const dispose = d.instrument(el);

    el.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    el.dispatchEvent(new CompositionEvent("compositionstart", { data: "" }));
    el.dispatchEvent(new CompositionEvent("compositionend", { data: "a" }));
    d.logOnData("a");

    const kinds = d.entries.value.map((e) => e.kind);
    expect(kinds).toEqual(["keydown", "composition", "composition", "ondata"]);
    dispose();
    // After dispose, further DOM events are not recorded.
    el.dispatchEvent(new KeyboardEvent("keydown", { key: "b" }));
    expect(d.entries.value.map((e) => e.kind)).toEqual([
      "keydown",
      "composition",
      "composition",
      "ondata",
    ]);
  });

  it("reports a round-trip time pairing input -> output", () => {
    const d = createTerminalDiagnostics();
    d.enabled.value = true;
    d.logInputSent("l");
    vi.advanceTimersByTime(50);
    d.logOutput(1);
    const out = d.entries.value.find((e) => e.kind === "output");
    expect(out?.msg).toContain("rtt=");
  });

  it("clear() empties the log and resets pending echoes", () => {
    const d = createTerminalDiagnostics();
    d.enabled.value = true;
    d.logInputSent("a");
    d.logOnData("a");
    expect(d.entries.value.length).toBeGreaterThan(0);
    d.clear();
    expect(d.entries.value).toHaveLength(0);
    // The cleared pending echo means the next output has no RTT to report.
    d.logOutput(1);
    const out = d.entries.value.find((e) => e.kind === "output");
    expect(out?.msg).not.toContain("rtt=");
  });
});
