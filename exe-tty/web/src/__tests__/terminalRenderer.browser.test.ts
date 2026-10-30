// @vitest-environment node
// Opt-in REAL browsers + installed WASM, not terminal doubles. Run with:
// EXE_TERMINAL_RENDERER_BROWSER_TESTS=1 pnpm exec vitest run src/__tests__/terminalRenderer.browser.test.ts
// This isolates the adapter.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { chromium, webkit, type Browser, type Page } from "@playwright/test";
import { createServer, type Server } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import ts from "typescript";

const enabled = process.env.EXE_TERMINAL_RENDERER_BROWSER_TESTS === "1";
const root = resolve(import.meta.dirname, "../..");
const ESC = "\x1b";
const paint = (color: number) => `${ESC}[H${ESC}[${color}m${" ".repeat(20)}`;
const start = `${ESC}[?2026h`;
const end = `${ESC}[?2026l`;
let server: Server;
let origin: string;

// Serve the actual pinned library, plus our adapter transpiled without mocks.
beforeAll(async () => {
  if (!enabled) return;
  server = createServer(async (req, res) => {
    try {
      const path = new URL(req.url!, "http://localhost").pathname;
      let body: string | Buffer;
      let contentType = "text/javascript";
      if (path === "/") {
        contentType = "text/html";
        body = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1">
          <link rel="stylesheet" href="/xterm.css">
          <div id="term" style="position:relative;width:320px;height:160px"></div>
          <script type="module">
          import {Ghostty, Terminal, UrlRegexProvider} from '/ghostty.js';
          import {patchGhosttyPrototype} from '/lib/terminalRenderer';
          patchGhosttyPrototype(Terminal.prototype, UrlRegexProvider);
          const ghostty = await Ghostty.load('/ghostty.wasm');
          window.term = new Terminal({ghostty,cols:20,rows:8,cursorBlink:true,fontSize:15});
          window.sent = []; term.onData(data => sent.push(data));
          term.open(document.querySelector('#term')); window.ready = true;
          </script>`;
      } else if (path.startsWith("/lib/")) {
        const source = await readFile(resolve(root, `src${path}.ts`), "utf8");
        body = ts.transpileModule(source, {
          compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
        }).outputText;
      } else {
        const files: Record<string, string> = {
          "/ghostty.js": "ghostty-web/dist/ghostty-web.js",
          "/ghostty.wasm": "ghostty-web/ghostty-vt.wasm",
          "/xterm.js": "@xterm/xterm/lib/xterm.mjs",
          "/xterm.css": "@xterm/xterm/css/xterm.css",
        };
        if (!files[path]) {
          res.writeHead(404).end();
          return;
        }
        if (path.endsWith(".wasm")) contentType = "application/wasm";
        if (path.endsWith(".css")) contentType = "text/css";
        body = await readFile(resolve(root, "node_modules", files[path]));
      }
      res.writeHead(200, { "Content-Type": contentType }).end(body);
    } catch (error) {
      res.writeHead(500).end(String(error));
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});
afterAll(async () => {
  if (server) await new Promise<void>((resolve) => server.close(() => resolve()));
});

async function frames(page: Page) {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}
async function write(page: Page, data: string, bytes = false) {
  await page.evaluate(
    ({ data, bytes }) => {
      const term = (window as unknown as { term: { write(data: string | Uint8Array): void } }).term;
      term.write(bytes ? new TextEncoder().encode(data) : data);
    },
    { data, bytes },
  );
}
async function pixel(page: Page) {
  return page.evaluate(() => {
    const canvas = document.querySelector("#term canvas") as HTMLCanvasElement;
    return Array.from(canvas.getContext("2d")!.getImageData(2, 2, 1, 1).data);
  });
}

for (const browserType of [chromium, webkit]) {
  describe.runIf(enabled)(`${browserType.name()} real Ghostty adapter`, () => {
    let browser: Browser;
    beforeAll(async () => {
      browser = await browserType.launch();
    });
    afterAll(async () => {
      await browser?.close();
    });

    async function withTerminal(run: (page: Page) => Promise<void>, mobile = false) {
      const page = await browser.newPage(
        mobile
          ? {
              viewport: { width: 390, height: 844 },
              isMobile: true,
              hasTouch: true,
              deviceScaleFactor: 3,
            }
          : {},
      );
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      try {
        await page.goto(origin);
        await page.waitForFunction("window.ready");
        await write(page, `${ESC}[?25l${paint(41)}`);
        await frames(page);
        await run(page);
        expect(errors).toEqual([]);
      } finally {
        await page.close();
      }
    }

    for (const mobile of [false, true]) {
      it(`routes canvas-click focus to the input that receives Unicode (${mobile ? "mobile" : "desktop"})`, async () => {
        await withTerminal(async (page) => {
          const canvas = page.locator("#term canvas");
          if (mobile) await canvas.tap({ position: { x: 30, y: 30 } });
          else await canvas.click({ position: { x: 30, y: 30 } });
          await page.keyboard.type("azAZ09 -_/");
          for (const key of ["ArrowLeft", "Control+a", "Backspace", "Enter"])
            await page.keyboard.press(key);
          expect(await page.evaluate("sent.join('')")).toBe(`azAZ09 -_/${ESC}[D\x01\x7f\r`);
          await page.evaluate("sent.length = 0");
          await page.keyboard.insertText("café 界 🙂 a\u0301");
          expect(
            await page.evaluate("({active: document.activeElement.tagName, data: sent.join('')})"),
          ).toEqual({ active: "TEXTAREA", data: "café 界 🙂 a\u0301" });
          // SelectionManager focuses the host directly, bypassing Terminal.focus.
          await page.evaluate("document.querySelector('#term').focus(); sent.length = 0");
          await page.keyboard.insertText("界");
          expect(await page.evaluate("sent.join('')")).toBe("界");
        }, mobile);
      });

      it(`holds whole/split updates, preserves replies and releases every frame (${mobile ? "mobile" : "desktop"})`, async () => {
        await withTerminal(async (page) => {
          const red = await pixel(page);
          await write(page, start + paint(42));
          await frames(page);
          expect(await pixel(page)).toEqual(red);
          await write(page, `${ESC}[5n${ESC}[?2026$p`);
          expect(await page.evaluate("sent.join('')")).toBe(`${ESC}[0n`); // DECRQM remains unsupported.
          await write(page, end);
          await frames(page);
          const green = await pixel(page);
          expect(green).not.toEqual(red);

          await write(page, `${ESC}[?20`, true);
          await write(page, `26h${paint(44)}`, true);
          await frames(page);
          expect(await pixel(page)).toEqual(green);
          await write(page, `${ESC}[?202`, true);
          await frames(page);
          expect(await pixel(page)).toEqual(green);
          await write(page, "6l", true);
          await frames(page);
          const blue = await pixel(page);
          expect(blue).not.toEqual(green);
          await frames(page);
          expect(await pixel(page)).toEqual(blue); // release didn't consume/lose the final frame.
          await write(page, start + paint(42) + end);
          await frames(page);
          expect(await pixel(page)).toEqual(green); // complete update within one write.
        }, mobile);
      });
    }

    it.skipIf(browserType !== chromium)(
      "commits native Chromium IME after clicking the canvas",
      async () => {
        await withTerminal(async (page) => {
          await page.locator("#term canvas").click({ position: { x: 30, y: 30 } });
          const cdp = await page.context().newCDPSession(page);
          try {
            await cdp.send("Input.imeSetComposition", {
              text: "ni",
              selectionStart: 2,
              selectionEnd: 2,
            });
            expect(await page.evaluate("sent.join('')")).toBe("");
            await cdp.send("Input.insertText", { text: "你" });
            expect(await page.evaluate("sent.join('')")).toBe("你");
          } finally {
            await cdp.detach();
          }
        });
      },
    );

    it("pastes from the native clipboard after clicking the canvas", async () => {
      await withTerminal(async (page) => {
        if (browserType === chromium)
          await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
        // WebKit requires a user gesture for clipboard writes. Use the same
        // trusted canvas click as the user, not a synthetic ClipboardEvent.
        await page.evaluate(() => {
          document.querySelector("#term canvas")!.addEventListener(
            "click",
            () => {
              (window as unknown as { clipboardWrite: Promise<void> }).clipboardWrite =
                navigator.clipboard.writeText("paste line one\n界🙂 line two");
            },
            { once: true },
          );
        });
        await page.locator("#term canvas").click({ position: { x: 30, y: 30 } });
        await page.evaluate("window.clipboardWrite");
        await write(page, `${ESC}[?2004h`);
        await page.keyboard.press(process.platform === "darwin" ? "Meta+v" : "Control+v");
        await expect
          .poll(() => page.evaluate("sent.join('')"))
          .toBe(`${ESC}[200~paste line one\n界🙂 line two${ESC}[201~`);
      });
    });

    // ghostty-web finds URLs a row at a time: on a URL that soft-wraps (an
    // OAuth sign-in link), Ctrl/Cmd+click on the first row opened that row's
    // part of it, and on the others nothing.
    it("opens the whole of a wrapped URL from any of its rows", async () => {
      await withTerminal(async (page) => {
        await page.evaluate(() => {
          const opened: string[] = [];
          Object.assign(window, { opened });
          window.open = (url?: string | URL) => (opened.push(String(url)), null);
        });
        const url = "https://example.com/abcdefghijklmnopqrstuvwxyz";
        const box = (await page.locator("#term canvas").boundingBox())!;
        const modifier = process.platform === "darwin" ? "Meta" : "Control";
        const click = async (row: number) => {
          await page.evaluate("opened.length = 0");
          const x = box.x + 4.5 * (box.width / 20);
          const y = box.y + (row + 0.5) * (box.height / 8);
          await page.mouse.move(x, y);
          await page.keyboard.down(modifier);
          await page.mouse.click(x, y);
          await page.keyboard.up(modifier);
          await frames(page);
          return page.evaluate("opened");
        };
        await write(page, `${ESC}[0m${ESC}[H${ESC}[2Jgo ${url} then\r\nnext`);
        await frames(page);
        for (const row of [0, 1, 2]) expect(await click(row)).toEqual([url]);
        expect(await click(3)).toEqual([]);
        // An OSC 8 hyperlink whose text is that URL opens its own target,
        // whichever row is clicked first.
        const target = "https://target.example/";
        await write(
          page,
          `${ESC}[H${ESC}[2J${ESC}]8;;${target}${ESC}\\go ${url} then${ESC}]8;;${ESC}\\`,
        );
        await frames(page);
        for (const row of [1, 0, 2]) expect(await click(row)).toEqual([target]);
        // A resize reflows the URL onto one row, and a link found before it
        // no longer covers the row below, though nothing has been written.
        await write(page, `${ESC}[H${ESC}[2Jgo ${url} then\r\nnext`);
        expect(await click(1)).toEqual([url]);
        await page.evaluate("term.resize(60, 8)");
        await frames(page);
        expect(
          await page.evaluate(
            "term.wasmTerm.getLine(1).map(c => String.fromCodePoint(c.codepoint || 32)).join('').trim()",
          ),
        ).toBe("next");
        expect(await click(1)).toEqual([]);
        expect(await click(0)).toEqual([url]);
      });
    });

    it("does not steal focus when wheel scrolling an unfocused terminal", async () => {
      await withTerminal(async (page) => {
        await page.evaluate(() => {
          const other = document.createElement("input");
          other.id = "outside";
          document.body.appendChild(other);
          other.focus();
        });
        await page.locator("#term canvas").hover();
        await page.mouse.wheel(0, 80);
        await frames(page);
        expect(await page.evaluate("document.activeElement.id")).toBe("outside");
      });
    });

    it("preserves dirty rows across a held multi-row update", async () => {
      await withTerminal(async (page) => {
        const sampleRows = () =>
          page.evaluate(() => {
            const canvas = document.querySelector("#term canvas") as HTMLCanvasElement;
            const height = canvas.height / 8;
            return [0, 3, 7].map((row) =>
              Array.from(canvas.getContext("2d")!.getImageData(2, row * height + 2, 1, 1).data),
            );
          });
        const frame =
          paint(42) +
          `${ESC}[4;1H${ESC}[44m${" ".repeat(20)}${ESC}[8;1H${ESC}[43m${" ".repeat(20)}`;
        await write(page, frame);
        await frames(page);
        const reference = await sampleRows();
        await write(page, `${ESC}[41m${ESC}[2J`);
        await frames(page);
        const before = await sampleRows();
        await write(page, start + frame);
        await frames(page);
        expect(await sampleRows()).toEqual(before);
        await write(page, end);
        await frames(page);
        expect(await sampleRows()).toEqual(reference);
      });
    });

    it("holds alternate-screen switches and forces the release repaint", async () => {
      await withTerminal(async (page) => {
        const red = await pixel(page);
        await write(page, start + `${ESC}[?1049h` + paint(42));
        await frames(page);
        expect(await pixel(page)).toEqual(red);
        await write(page, end);
        await frames(page);
        const green = await pixel(page);
        expect(green).not.toEqual(red);
        await write(page, start + `${ESC}[?1049l`);
        await frames(page);
        expect(await pixel(page)).toEqual(green);
        await write(page, end);
        await frames(page);
        expect(await pixel(page)).toEqual(red);
      });
    });

    it("bounds a stuck update, keeps replies flowing, and rearms on a parsed reset", async () => {
      await withTerminal(async (page) => {
        const red = await pixel(page);
        await write(page, start + paint(42));
        await frames(page);
        expect(await pixel(page)).toEqual(red);
        await expect.poll(() => pixel(page), { timeout: 2500 }).not.toEqual(red);
        const green = await pixel(page);
        expect(await page.evaluate("term.getMode(2026)")).toBe(true); // don't forge emulator state.
        await write(page, paint(44));
        await frames(page);
        const blue = await pixel(page);
        expect(blue).not.toEqual(green); // fail open until a real reset.
        await write(page, end);
        await write(page, start + paint(42)); // no intervening RAF: write-time observation matters.
        await frames(page);
        expect(await pixel(page)).toEqual(blue);
        await write(page, end);
        await frames(page);
        expect(await pixel(page)).toEqual(green);
      });
    });

    it("repaints rather than leaving a blank canvas when resized during a hold", async () => {
      await withTerminal(async (page) => {
        const red = await pixel(page);
        await write(page, start + paint(42));
        await frames(page);
        expect(await pixel(page)).toEqual(red);
        await page.evaluate("term.resize(24, 9)");
        await frames(page);
        const green = await pixel(page);
        expect(green).not.toEqual(red);
        expect(await page.evaluate("[term.cols, term.rows, term.getMode(2026)]")).toEqual([
          24,
          9,
          true,
        ]);
        await write(page, end);
        await frames(page);
        expect(await pixel(page)).toEqual(green);
      });
    });

    it("disposes a held update and reuses the same host for xterm input and wheel", async () => {
      await withTerminal(async (page) => {
        await page.clock.install();
        await write(page, start + paint(42));
        await page.evaluate(`(async () => {
          const host = document.querySelector('#term');
          const oldInput = host.querySelector('textarea');
          const oldFocus = oldInput.focus.bind(oldInput);
          window.disposedInputFocusCalls = 0;
          oldInput.focus = (...args) => { window.disposedInputFocusCalls++; oldFocus(...args); };
          term.dispose(); host.focus(); host.innerHTML = '';
          const {Terminal} = await import('/xterm.js');
          window.term = new Terminal({cols:20,rows:8}); window.sent = [];
          term.onData(data => sent.push(data)); term.open(document.querySelector('#term')); term.focus();
          term.write('${ESC}[?1049h${ESC}[?1000h${ESC}[?1006h');
        })()`);
        await page.clock.fastForward(1200); // disposed timeout must not access the freed WASM.
        expect(await page.evaluate("window.disposedInputFocusCalls")).toBe(0);
        expect(
          await page
            .locator(".xterm-helper-textarea")
            .evaluate((el) => el === document.activeElement),
        ).toBe(true);
        await page.keyboard.type("ok");
        await page
          .locator(".xterm-screen")
          .dispatchEvent("wheel", { deltaY: 60, clientX: 20, clientY: 20 });
        expect(await page.evaluate("sent.join('')")).toBe(`ok${ESC}[<65;2;1M`);
      });
    });
  });
}
