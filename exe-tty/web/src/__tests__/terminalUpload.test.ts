import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { uploadFile } from "../lib/terminalUpload";

// A small fake File-ish object; the upload helper only reads .name and .type.
function fakeFile(name: string, type = ""): File {
  return { name, type } as unknown as File;
}

describe("uploadFile (fetch path)", () => {
  it("POSTs to /terminal/upload with filename + dir and returns the path", async () => {
    const fetchImpl = vi.fn(async (url: string, init?: RequestInit) => {
      expect(url).toBe("http://x/terminal/upload?filename=a%20b.txt&dir=%2Ftmp%2Fsub");
      expect(init?.method).toBe("POST");
      expect((init?.headers as Record<string, string>)["Content-Type"]).toBe("text/plain");
      return new Response(JSON.stringify({ path: "/tmp/sub/a b.txt" }), { status: 200 });
    });
    const res = await uploadFile(
      "http://x",
      fakeFile("a b.txt", "text/plain"),
      "/tmp/sub",
      fetchImpl as unknown as typeof fetch,
    );
    expect(res.path).toBe("/tmp/sub/a b.txt");
    expect(fetchImpl).toHaveBeenCalledOnce();
  });

  it("omits the dir param when dir is blank and defaults the content type", async () => {
    const fetchImpl = vi.fn(async (url: string, init?: RequestInit) => {
      expect(url).toBe("http://x/terminal/upload?filename=f.bin");
      expect((init?.headers as Record<string, string>)["Content-Type"]).toBe(
        "application/octet-stream",
      );
      return new Response(JSON.stringify({ path: "/tmp/f.bin" }), { status: 200 });
    });
    const res = await uploadFile(
      "http://x",
      fakeFile("f.bin"),
      "   ",
      fetchImpl as unknown as typeof fetch,
    );
    expect(res.path).toBe("/tmp/f.bin");
  });

  it("surfaces a JSON {error} body on failure", async () => {
    const fetchImpl = vi.fn(
      async () => new Response(JSON.stringify({ error: "nope" }), { status: 409 }),
    );
    await expect(
      uploadFile("http://x", fakeFile("f"), "", fetchImpl as unknown as typeof fetch),
    ).rejects.toThrow("nope");
  });

  it("uses a friendly message for 413 with no body", async () => {
    const fetchImpl = vi.fn(async () => new Response("", { status: 413 }));
    await expect(
      uploadFile("http://x", fakeFile("f"), "", fetchImpl as unknown as typeof fetch),
    ).rejects.toThrow(/larger than the upload limit/);
  });

  it("surfaces a plain-text error body", async () => {
    const fetchImpl = vi.fn(async () => new Response("boom", { status: 500 }));
    await expect(
      uploadFile("http://x", fakeFile("f"), "", fetchImpl as unknown as typeof fetch),
    ).rejects.toThrow("boom");
  });

  it("throws when the response is OK but has no path", async () => {
    const fetchImpl = vi.fn(async () => new Response("{}", { status: 200 }));
    await expect(
      uploadFile("http://x", fakeFile("f"), "", fetchImpl as unknown as typeof fetch),
    ).rejects.toThrow(/no path/);
  });
});

// Minimal XMLHttpRequest stand-in so we can drive the progress branch
// deterministically without a real network or DOM XHR.
class FakeXHR {
  static last: FakeXHR | null = null;
  method = "";
  url = "";
  status = 0;
  responseText = "";
  withCredentials = false;
  upload: {
    onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null;
  } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  headers: Record<string, string> = {};
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
    FakeXHR.last = this;
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  send() {
    // Simulate two progress ticks then a successful load.
    this.upload.onprogress?.({ lengthComputable: true, loaded: 50, total: 100 });
    this.upload.onprogress?.({ lengthComputable: true, loaded: 100, total: 100 });
    this.status = 200;
    this.responseText = JSON.stringify({ path: "/tmp/up.txt" });
    this.onload?.();
  }
}

describe("uploadFile (XHR progress path)", () => {
  const realXHR = globalThis.XMLHttpRequest;
  const realSend = FakeXHR.prototype.send;
  beforeEach(() => {
    // @ts-expect-error override for the test
    globalThis.XMLHttpRequest = FakeXHR;
    FakeXHR.last = null;
  });
  afterEach(() => {
    globalThis.XMLHttpRequest = realXHR;
    FakeXHR.prototype.send = realSend;
  });

  it("reports progress and resolves with the path", async () => {
    const fractions: number[] = [];
    const res = await uploadFile("http://x", fakeFile("up.txt", "text/plain"), "/tmp", fetch, (f) =>
      fractions.push(f),
    );
    expect(res.path).toBe("/tmp/up.txt");
    expect(fractions).toEqual([0.5, 1]);
    // It used XHR (not withCredentials, to match same-origin fetch semantics).
    expect(FakeXHR.last?.method).toBe("POST");
    expect(FakeXHR.last?.withCredentials).toBe(false);
    expect(FakeXHR.last?.headers["Content-Type"]).toBe("text/plain");
  });

  it("rejects with a message on a network error", async () => {
    // Make send() trigger onerror instead.
    FakeXHR.prototype.send = function (this: FakeXHR) {
      this.onerror?.();
    };
    await expect(uploadFile("http://x", fakeFile("f"), "", fetch, () => {})).rejects.toThrow(
      /network error/,
    );
  });
});
