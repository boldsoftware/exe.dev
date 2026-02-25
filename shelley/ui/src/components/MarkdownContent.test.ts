// Unit tests for markdown sanitization.
// Run with: tsx src/components/MarkdownContent.test.ts

import { JSDOM } from "jsdom";
import { Marked } from "marked";
import DOMPurify from "dompurify";

// Set up a DOM environment for DOMPurify.
const dom = new JSDOM("");
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const purify = DOMPurify(dom.window as any);

// Mirror the exact config from MarkdownContent.tsx.
const markedInstance = new Marked({ gfm: true, breaks: true });

purify.addHook("afterSanitizeAttributes", (node: Element) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
  if (node.tagName === "INPUT" && node.getAttribute("type") !== "checkbox") {
    node.remove();
  }
});

const ALLOWED_TAGS = [
  "p",
  "br",
  "strong",
  "em",
  "code",
  "pre",
  "blockquote",
  "ul",
  "ol",
  "li",
  "a",
  "h1",
  "h2",
  "h3",
  "h4",
  "h5",
  "h6",
  "hr",
  "table",
  "thead",
  "tbody",
  "tr",
  "th",
  "td",
  "del",
  "input",
  "span",
  "div",
];

const ALLOWED_ATTR = ["href", "target", "rel", "type", "checked", "disabled", "class"];

function renderMarkdown(text: string): string {
  const raw = markedInstance.parse(text, { async: false }) as string;
  return purify.sanitize(raw, { ALLOWED_TAGS, ALLOWED_ATTR });
}

interface TestCase {
  name: string;
  input: string;
  // A substring that MUST appear in the output.
  mustContain?: string[];
  // A substring that must NOT appear in the output.
  mustNotContain?: string[];
}

const testCases: TestCase[] = [
  // ---- XSS vectors ----
  {
    name: "script tag is stripped",
    input: "<script>alert('xss')</script>hello",
    mustContain: ["hello"],
    mustNotContain: ["<script", "alert"],
  },
  {
    name: "img tag with onerror is stripped",
    input: "<img src=x onerror=alert(1)>",
    mustNotContain: ["<img", "onerror", "alert"],
  },
  {
    name: "svg onload is stripped",
    input: "<svg onload=alert(1)>",
    mustNotContain: ["<svg", "onload", "alert"],
  },
  {
    name: "iframe is stripped",
    input: '<iframe src="https://evil.com"></iframe>hello',
    mustContain: ["hello"],
    mustNotContain: ["<iframe", "evil.com"],
  },
  {
    name: "event handler attributes are stripped",
    input: "<div onclick=alert(1)>click me</div>",
    mustContain: ["click me"],
    mustNotContain: ["onclick", "alert"],
  },
  {
    name: "javascript: href is sanitized",
    input: '<a href="javascript:alert(1)">click</a>',
    mustContain: ["click"],
    mustNotContain: ["javascript:"],
  },
  {
    name: "data: href is sanitized",
    input: '<a href="data:text/html,<script>alert(1)</script>">click</a>',
    mustContain: ["click"],
    mustNotContain: ["data:text/html"],
  },
  {
    name: "style attribute is stripped",
    input: '<div style="background:url(javascript:alert(1))">test</div>',
    mustContain: ["test"],
    mustNotContain: ["style="],
  },
  {
    name: "nested script in markdown",
    input: "**bold** <script>alert('xss')</script> *italic*",
    mustContain: ["<strong>bold</strong>", "<em>italic</em>"],
    mustNotContain: ["<script", "alert"],
  },
  {
    name: "markdown image syntax is rendered then stripped",
    input: "![alt text](https://evil.com/tracker.png)",
    mustNotContain: ["<img", "tracker.png"],
  },
  {
    name: "object tag is stripped",
    input: '<object data="evil.swf"></object>test',
    mustContain: ["test"],
    mustNotContain: ["<object", "evil.swf"],
  },
  {
    name: "embed tag is stripped",
    input: '<embed src="evil.swf">test',
    mustContain: ["test"],
    mustNotContain: ["<embed", "evil.swf"],
  },
  {
    name: "form tag is stripped",
    input: '<form action="https://evil.com"><input type="submit"></form>',
    mustNotContain: ["<form", "action", "evil.com"],
  },
  {
    name: "base tag is stripped",
    input: '<base href="https://evil.com">',
    mustNotContain: ["<base", "evil.com"],
  },
  {
    name: "meta refresh is stripped",
    input: '<meta http-equiv="refresh" content="0;url=https://evil.com">',
    mustNotContain: ["<meta", "refresh", "evil.com"],
  },

  // ---- Markdown rendering ----
  {
    name: "bold renders correctly",
    input: "**hello**",
    mustContain: ["<strong>hello</strong>"],
  },
  {
    name: "italic renders correctly",
    input: "*world*",
    mustContain: ["<em>world</em>"],
  },
  {
    name: "inline code renders correctly",
    input: "`code here`",
    mustContain: ["<code>code here</code>"],
  },
  {
    name: "heading renders correctly",
    input: "# Title",
    mustContain: ["<h1>Title</h1>"],
  },
  {
    name: "link renders correctly with target=_blank",
    input: "[click](https://example.com)",
    mustContain: ['href="https://example.com"', 'target="_blank"', 'rel="noopener noreferrer"'],
  },
  {
    name: "unordered list renders correctly",
    input: "- item 1\n- item 2",
    mustContain: ["<ul>", "<li>item 1</li>", "<li>item 2</li>"],
  },
  {
    name: "code block renders correctly",
    input: "```\nconst x = 1;\n```",
    mustContain: ["<pre>", "<code>", "const x = 1;"],
  },
  {
    name: "blockquote renders correctly",
    input: "> quoted text",
    mustContain: ["<blockquote>", "quoted text"],
  },
  {
    name: "table renders correctly",
    input: "| A | B |\n|---|---|\n| 1 | 2 |",
    mustContain: ["<table>", "<th>A</th>", "<td>1</td>"],
  },
  {
    name: "strikethrough renders correctly",
    input: "~~deleted~~",
    mustContain: ["<del>deleted</del>"],
  },

  // ---- Input restriction ----
  {
    name: "text input is stripped (phishing prevention)",
    input: '<input type="text" placeholder="Enter password">',
    mustNotContain: ["<input", 'type="text"', "Enter password"],
  },
  {
    name: "password input is stripped",
    input: '<input type="password">',
    mustNotContain: ["<input", 'type="password"'],
  },
  {
    name: "checkbox input is allowed (GFM task lists)",
    input: "- [x] done\n- [ ] todo",
    mustContain: ['type="checkbox"'],
  },

  // ---- Edge cases ----
  {
    name: "HTML entities in markdown are safe",
    input: "Use `<script>` tags carefully",
    mustContain: ["&lt;script&gt;"],
    mustNotContain: ["<script>"],
  },
  {
    name: "mixed markdown and HTML injection",
    input: "# Hello <img src=x onerror=alert(1)>\n\nSafe **bold** text",
    mustContain: ["<h1>", "Hello", "<strong>bold</strong>"],
    mustNotContain: ["<img", "onerror", "alert"],
  },
  {
    name: "SVG with embedded script is stripped",
    input: "<svg><script>alert(1)</script></svg>",
    mustNotContain: ["<svg", "<script", "alert"],
  },
  {
    name: "markdown link with javascript protocol",
    input: "[click me](javascript:alert(document.cookie))",
    mustContain: ["click me"],
    mustNotContain: ["javascript:"],
  },
  {
    name: "empty input returns empty or whitespace",
    input: "",
    mustNotContain: ["<script", "<img"],
  },
];

function runTests(): { passed: number; failed: number; failures: string[] } {
  let passed = 0;
  let failed = 0;
  const failures: string[] = [];

  for (const tc of testCases) {
    const output = renderMarkdown(tc.input);
    let ok = true;
    const problems: string[] = [];

    for (const s of tc.mustContain ?? []) {
      if (!output.includes(s)) {
        ok = false;
        problems.push(`expected to contain: ${JSON.stringify(s)}`);
      }
    }
    for (const s of tc.mustNotContain ?? []) {
      if (output.includes(s)) {
        ok = false;
        problems.push(`must NOT contain: ${JSON.stringify(s)}`);
      }
    }

    if (ok) {
      passed++;
    } else {
      failed++;
      failures.push(
        `FAIL: ${tc.name}\n  Input:  ${JSON.stringify(tc.input)}\n  Output: ${JSON.stringify(output)}\n  ${problems.join("\n  ")}`,
      );
    }
  }

  return { passed, failed, failures };
}

// Export for potential future use by a generic runner.
export { testCases, runTests };

// ---- Self-running ----
const { passed, failed, failures } = runTests();

console.log(`\nMarkdown Sanitization Tests: ${passed} passed, ${failed} failed\n`);

if (failures.length > 0) {
  console.log("Failures:");
  for (const f of failures) {
    console.log(f);
    console.log("");
  }
  process.exit(1);
}

console.log("All tests passed!");
process.exit(0);
