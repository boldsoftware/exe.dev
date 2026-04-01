import { JSDOM } from "jsdom";
import DOMPurify from "dompurify";

// Set up DOM for DOMPurify before importing ansi utils.
// In the browser DOMPurify auto-detects window; in Node we must provide it.
const dom = new JSDOM("");
const g = globalThis as Record<string, unknown>;
g.window = dom.window;
g.document = dom.window.document;

// DOMPurify in Node requires explicit initialization with a window.
// Monkey-patch so the module-level `DOMPurify.sanitize` works.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const purify = DOMPurify(dom.window as any);
Object.assign(DOMPurify, { sanitize: purify.sanitize.bind(purify) });

import { ansiToHtml, stripAnsi } from "./ansi";

let passed = 0;
let failed = 0;

function assert(condition: boolean, msg: string) {
  if (condition) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: ${msg}`);
  }
}

// ansiToHtml: no escape sequences → empty string
assert(ansiToHtml("hello world") === "", "plain text returns empty");
assert(ansiToHtml("") === "", "empty string returns empty");

// ansiToHtml: ANSI codes produce HTML
const html = ansiToHtml("\x1b[32mgreen\x1b[0m");
assert(html.includes("green"), "contains the text 'green'");
assert(html.includes("<span"), "contains a span tag");
assert(html.includes("color"), "contains color styling");

// ansiToHtml: bold
const boldHtml = ansiToHtml("\x1b[1mbold\x1b[0m");
assert(boldHtml.includes("bold"), "bold text present");
assert(boldHtml.includes("<"), "contains HTML tags");

// stripAnsi: removes escape sequences
assert(stripAnsi("\x1b[32mgreen\x1b[0m") === "green", "strips green code");
assert(stripAnsi("hello") === "hello", "plain text unchanged");
assert(
  stripAnsi("\x1b[0m\x1b[32mTask\x1b[0m \x1b[36mdev:copy\x1b[0m") === "Task dev:copy",
  "strips deno-style output",
);

// stripAnsi: cursor show/hide sequences
assert(stripAnsi("\x1b[?25lhidden\x1b[?25h") === "hidden", "strips cursor hide/show sequences");

// ansiToHtml: mixed plain and ANSI
const mixed = ansiToHtml("hello \x1b[31mred\x1b[0m world");
assert(mixed.includes("hello"), "mixed: includes plain text before");
assert(mixed.includes("red"), "mixed: includes colored text");
assert(mixed.includes("world"), "mixed: includes plain text after");

// ansiToHtml: sanitizes malicious HTML in the source
const xss = ansiToHtml("\x1b[32m<script>alert(1)</script>\x1b[0m");
assert(!xss.includes("<script>"), "XSS: script tags are stripped");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
