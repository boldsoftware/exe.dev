import { describe, it, expect, vi, afterEach } from "vitest";
import { linkHandler } from "../lib/terminalRenderer";

// Every Terminal we construct must be given a linkHandler. Without one,
// xterm.js answers a click on an OSC 8 link — which programs emit for the
// long URLs they ask people to open — with a blocking confirm() warning
// the link "could potentially be dangerous". These are links this server
// just printed, and the WebLinksAddon opens the ones it finds by scanning
// text without asking, so the dialog is both alarming and inconsistent.
describe("terminal linkHandler", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("opens the link without prompting", () => {
    const open = vi.fn();
    const confirm = vi.fn(() => false);
    vi.stubGlobal("open", open);
    vi.stubGlobal("confirm", confirm);

    const url = "https://exe.dev/auth/verify?token=ABCDEF";
    linkHandler.activate(new MouseEvent("click"), url);

    expect(confirm).not.toHaveBeenCalled();
    expect(open).toHaveBeenCalledOnce();
    // noopener keeps the opened page from reaching back through window.opener.
    const [got, target, features] = open.mock.calls[0];
    expect(got).toBe(url);
    expect(target).toBe("_blank");
    expect(features).toContain("noopener");
  });
});
