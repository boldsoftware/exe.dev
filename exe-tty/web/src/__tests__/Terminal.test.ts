import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import PrimeVue from "primevue/config";
import ToastService from "primevue/toastservice";
import Terminal from "../Terminal.vue";
import SettingsMenu from "../components/SettingsMenu.vue";
import {
  loadTerminalBackend,
  type RendererKind,
  type TerminalBackend,
  type TerminalLike,
} from "../lib/terminalRenderer";

vi.mock("../lib/terminalRenderer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../lib/terminalRenderer")>()),
  loadTerminalBackend: vi.fn(),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

const mountedRenderers: RendererKind[] = [];
function backend(kind: RendererKind): TerminalBackend {
  return {
    kind,
    Terminal: class implements TerminalLike {
      cols = 80;
      rows = 24;
      constructor(public options: Record<string, unknown>) {}
      loadAddon() {}
      open() {
        mountedRenderers.push(kind);
      }
      focus() {}
      write() {}
      paste() {}
      hasSelection() {
        return false;
      }
      getSelection() {
        return "";
      }
      clearSelection() {}
      onData() {}
      attachCustomKeyEventHandler() {}
      resize() {}
      dispose() {}
    },
    FitAddon: class {
      fit() {}
      proposeDimensions() {
        return { cols: 80, rows: 24 };
      }
    },
    WebLinksAddon: class {},
  };
}

const sockets: string[] = [];
const socketURLs: URL[] = [];
class Socket {
  static OPEN = 1;
  static CONNECTING = 0;
  readyState = Socket.CONNECTING;
  constructor(url: string) {
    socketURLs.push(new URL(url));
    sockets.push(new URL(url).searchParams.get("name")!);
  }
  close() {}
  send() {}
}

const loads: Array<{ kind: RendererKind } & ReturnType<typeof deferred<TerminalBackend>>> = [];
function finishLoad(index: number) {
  const load = loads[index]!;
  load.resolve(backend(load.kind));
}
function mountTerminal() {
  return mount(Terminal, {
    global: {
      plugins: [PrimeVue, ToastService],
      stubs: { SettingsMenu: true, TerminalDiagnostics: true },
    },
  });
}
function sessions() {
  return new Response(
    JSON.stringify({ sessions: ["thistle", "crimson", "lime"].map((name) => ({ name })) }),
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  sessionStorage.clear();
  history.replaceState({}, "", "/pages/terminal.html");
  loads.length = 0;
  sockets.length = 0;
  socketURLs.length = 0;
  mountedRenderers.length = 0;
  vi.mocked(loadTerminalBackend).mockImplementation((kind = "ghostty") => {
    const load = { kind, ...deferred<TerminalBackend>() };
    loads.push(load);
    return load.promise;
  });
  vi.stubGlobal("WebSocket", Socket);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => sessions()),
  );
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("terminal initialization ownership", () => {
  it.each(["explicit", "adopt", "mint"] as const)("connects once on %s startup", async (mode) => {
    if (mode === "explicit") history.replaceState({}, "", "?name=thistle");
    if (mode === "mint")
      vi.mocked(fetch).mockImplementation(async () => new Response('{"sessions":[]}'));
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    expect(sockets).toHaveLength(1);
    expect(sockets[0]).toBe(new URL(location.href).searchParams.get("name"));
    if (mode !== "mint") expect(sockets).toEqual([mode === "explicit" ? "thistle" : "lime"]);
    expect(mountedRenderers).toEqual(["ghostty"]);
    wrapper.unmount();
  });

  it("a new-session click during renderer loading owns the only connect", async () => {
    const wrapper = mountTerminal();
    await flushPromises();
    expect(loads).toHaveLength(1);
    await wrapper.get(".tab-new").trigger("click");
    await flushPromises();
    const chosen = new URL(location.href).searchParams.get("name");
    expect(chosen).toBeTruthy();
    expect(loads).toHaveLength(2);
    finishLoad(0);
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual([chosen]);
    expect(mountedRenderers).toEqual(["ghostty"]);
    wrapper.unmount();
  });

  it("a failed WASM load falling back to xterm still owns one startup", async () => {
    const wrapper = mountTerminal();
    await flushPromises();
    loads[0]!.resolve(backend("xterm"));
    await flushPromises();
    expect(loads).toHaveLength(1);
    expect(sockets).toEqual(["lime"]);
    expect(mountedRenderers).toEqual(["xterm"]);
    expect(localStorage.getItem("exe:terminal:renderer")).toBe("xterm");
    wrapper.unmount();
  });

  it("canceled tab remounts neither render nor connect the newer name", async () => {
    history.replaceState({}, "", "?name=thistle");
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    await wrapper.get('[data-session="crimson"]').trigger("click");
    await flushPromises();
    await wrapper.get('[data-session="lime"]').trigger("click");
    await flushPromises();
    expect(loads).toHaveLength(3);
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual(["thistle"]);
    expect(mountedRenderers).toEqual(["ghostty"]);
    finishLoad(2);
    await flushPromises();
    expect(sockets).toEqual(["thistle", "lime"]);
    expect(mountedRenderers).toEqual(["ghostty", "ghostty"]);
    wrapper.unmount();
  });

  it("a renderer change during startup owns initialization, even to the initial default kind", async () => {
    history.replaceState({}, "", "?name=thistle");
    const wrapper = mountTerminal();
    await flushPromises();
    wrapper.getComponent(SettingsMenu).vm.$emit("update:rendererKind", "xterm");
    await flushPromises();
    expect(loads.map((load) => load.kind)).toEqual(["ghostty", "xterm"]);
    finishLoad(0);
    await flushPromises();
    expect(sockets).toEqual([]);
    expect(mountedRenderers).toEqual([]);
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual(["thistle"]);
    expect(mountedRenderers).toEqual(["xterm"]);
    expect(localStorage.getItem("exe:terminal:renderer")).toBe("xterm");
    wrapper.unmount();
  });

  it("a renderer change supersedes a pending tab remount without double-connecting", async () => {
    history.replaceState({}, "", "?name=thistle");
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    await wrapper.get('[data-session="crimson"]').trigger("click");
    await flushPromises();
    wrapper.getComponent(SettingsMenu).vm.$emit("update:rendererKind", "xterm");
    await flushPromises();
    finishLoad(2);
    await flushPromises();
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual(["thistle", "crimson"]);
    expect(mountedRenderers).toEqual(["ghostty", "xterm"]);
    wrapper.unmount();
  });

  it("an explicit choice during active adoption connects before the lookup completes", async () => {
    const adoption = deferred<Response>();
    vi.mocked(fetch).mockImplementationOnce(() => adoption.promise);
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    expect(fetch).toHaveBeenCalledTimes(1);
    await wrapper.get(".tab-new").trigger("click");
    await flushPromises();
    const chosen = new URL(location.href).searchParams.get("name");
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual([chosen]);
    adoption.resolve(sessions());
    await flushPromises();
    expect(sockets).toEqual([chosen]);
    expect(new URL(location.href).searchParams.get("name")).toBe(chosen);
    wrapper.unmount();
  });

  it("adoption completing during an engine load waits for the winning renderer", async () => {
    const adoption = deferred<Response>();
    vi.mocked(fetch).mockImplementationOnce(() => adoption.promise);
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    wrapper.getComponent(SettingsMenu).vm.$emit("update:rendererKind", "xterm");
    await flushPromises();
    adoption.resolve(sessions());
    await flushPromises();
    expect(new URL(location.href).searchParams.get("name")).toBe("lime");
    expect(sockets).toEqual([]);
    finishLoad(1);
    await flushPromises();
    expect(sockets).toEqual(["lime"]);
    expect(mountedRenderers).toEqual(["ghostty", "xterm"]);
    wrapper.unmount();
  });

  it("unmounting cancels renderer loading and startup", async () => {
    const wrapper = mountTerminal();
    await flushPromises();
    wrapper.unmount();
    finishLoad(0);
    await flushPromises();
    expect(sockets).toEqual([]);
    expect(mountedRenderers).toEqual([]);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("unmounting during adoption abandons its result without connecting", async () => {
    const adoption = deferred<Response>();
    vi.mocked(fetch).mockImplementationOnce(() => adoption.promise);
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    wrapper.unmount();
    adoption.resolve(sessions());
    await flushPromises();
    expect(sockets).toEqual([]);
    expect(new URL(location.href).searchParams.get("name")).toBeNull();
    expect(fetch).toHaveBeenCalledTimes(1);
  });
});

// ?d=<dir> asks for a shell started in <dir>. The server only honours it when
// it creates a session, so the page forwards it on every socket and avoids
// adopting an existing session (whose shell is already somewhere else).
describe("working directory (?d=)", () => {
  async function start() {
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    return wrapper;
  }
  const dirs = () => socketURLs.map((u) => u.searchParams.get("d"));

  it("is forwarded to the socket for a named session", async () => {
    history.replaceState({}, "", "?name=thistle&d=/srv/my%20app");
    const wrapper = await start();
    expect(sockets).toEqual(["thistle"]);
    expect(dirs()).toEqual(["/srv/my app"]);
    wrapper.unmount();
  });

  it("mints a new session instead of adopting one", async () => {
    history.replaceState({}, "", "?d=/srv/app");
    const wrapper = await start();
    expect(sockets).toHaveLength(1);
    expect(["thistle", "crimson", "lime"]).not.toContain(sockets[0]);
    expect(dirs()).toEqual(["/srv/app"]);
    // The URL keeps d alongside the minted name, so a refresh re-attaches.
    const u = new URL(location.href);
    expect(u.searchParams.get("name")).toBe(sockets[0]);
    expect(u.searchParams.get("d")).toBe("/srv/app");
    wrapper.unmount();
  });

  it("applies to new tabs opened from the page", async () => {
    history.replaceState({}, "", "?name=thistle&d=/srv/app");
    const wrapper = await start();
    await wrapper.get(".tab-new").trigger("click");
    await flushPromises();
    finishLoad(1);
    await flushPromises();
    expect(sockets).toHaveLength(2);
    expect(dirs()).toEqual(["/srv/app", "/srv/app"]);
    wrapper.unmount();
  });

  it.each(["?name=thistle", "?name=thistle&d="])("is omitted for %s", async (search) => {
    history.replaceState({}, "", search);
    const wrapper = await start();
    expect(sockets).toEqual(["thistle"]);
    expect(socketURLs[0]!.searchParams.has("d")).toBe(false);
    wrapper.unmount();
  });
});

// The session-list toggle is a tab counter: the number of sessions in an
// outlined box.
describe("session list toggle", () => {
  async function start() {
    history.replaceState({}, "", "?name=thistle");
    const wrapper = mountTerminal();
    await flushPromises();
    finishLoad(0);
    await flushPromises();
    return wrapper;
  }
  const tabs = (wrapper: Awaited<ReturnType<typeof start>>) =>
    wrapper.findAll(".tab[data-session]").map((t) => t.attributes("data-session"));

  it("shows the session count and nothing else", async () => {
    const wrapper = await start();
    const toggle = wrapper.get(".tab-toggle");
    expect(toggle.text()).toBe("3");
    await wrapper.get(".tab-new").trigger("click");
    expect(toggle.text()).toBe("4");
    wrapper.unmount();
  });

  it("counts the session on screen before the list loads", async () => {
    vi.mocked(fetch).mockImplementation(() => new Promise<Response>(() => {}));
    const wrapper = await start();
    expect(wrapper.get(".tab-toggle").text()).toBe("1");
    wrapper.unmount();
  });

  it("caps the count at 99+ but labels the exact number", async () => {
    const many = Array.from({ length: 120 }, (_, i) => ({ name: i ? `s${i}` : "thistle" }));
    vi.mocked(fetch).mockImplementation(
      async () => new Response(JSON.stringify({ sessions: many })),
    );
    const wrapper = await start();
    const toggle = wrapper.get(".tab-toggle");
    expect(toggle.text()).toBe("99+");
    expect(toggle.attributes("aria-label")).toBe("Sessions (120)");
    wrapper.unmount();
  });

  it("opens and closes the session list", async () => {
    localStorage.setItem(`xterm:${location.hostname}:tabsCollapsed`, "1");
    const wrapper = await start();
    const toggle = wrapper.get(".tab-toggle");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(toggle.attributes("aria-label")).toBe("Sessions (3)");
    expect(toggle.attributes("title")).toBe("Show all sessions (3)");
    expect(tabs(wrapper)).toEqual([]);

    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("true");
    expect(toggle.attributes("title")).toBe("Hide session list (3)");
    expect(tabs(wrapper)).toEqual(["thistle", "crimson", "lime"]);
    expect(localStorage.getItem(`xterm:${location.hostname}:tabsCollapsed`)).toBe("0");

    await toggle.trigger("click");
    expect(toggle.attributes("aria-expanded")).toBe("false");
    expect(tabs(wrapper)).toEqual([]);
    wrapper.unmount();
  });
});
