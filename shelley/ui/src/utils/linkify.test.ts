import { parseLinks, LinkifyResult } from "./linkify";

interface TestCase {
  name: string;
  input: string;
  expected: LinkifyResult[];
}

const testCases: TestCase[] = [
  {
    name: "plain text with no URLs",
    input: "Hello world",
    expected: [{ type: "text", content: "Hello world" }],
  },
  {
    name: "simple http URL",
    input: "Check out http://example.com for more",
    expected: [
      { type: "text", content: "Check out " },
      { type: "link", content: "http://example.com", href: "http://example.com" },
      { type: "text", content: " for more" },
    ],
  },
  {
    name: "simple https URL",
    input: "Visit https://example.com today",
    expected: [
      { type: "text", content: "Visit " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: " today" },
    ],
  },
  {
    name: "URL with path",
    input: "See https://example.com/path/to/page for details",
    expected: [
      { type: "text", content: "See " },
      {
        type: "link",
        content: "https://example.com/path/to/page",
        href: "https://example.com/path/to/page",
      },
      { type: "text", content: " for details" },
    ],
  },
  {
    name: "URL with query parameters",
    input: "Link: https://example.com/search?q=test&page=1",
    expected: [
      { type: "text", content: "Link: " },
      {
        type: "link",
        content: "https://example.com/search?q=test&page=1",
        href: "https://example.com/search?q=test&page=1",
      },
    ],
  },
  {
    name: "URL with port",
    input: "Server at https://localhost:8080/api",
    expected: [
      { type: "text", content: "Server at " },
      {
        type: "link",
        content: "https://localhost:8080/api",
        href: "https://localhost:8080/api",
      },
    ],
  },
  {
    name: "URL followed by period (sentence end)",
    input: "Check https://example.com.",
    expected: [
      { type: "text", content: "Check " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "." },
    ],
  },
  {
    name: "URL followed by comma",
    input: "Visit https://example.com, then continue",
    expected: [
      { type: "text", content: "Visit " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ", then continue" },
    ],
  },
  {
    name: "URL followed by exclamation",
    input: "Wow https://example.com!",
    expected: [
      { type: "text", content: "Wow " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "!" },
    ],
  },
  {
    name: "URL followed by question mark",
    input: "Have you seen https://example.com?",
    expected: [
      { type: "text", content: "Have you seen " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "?" },
    ],
  },
  {
    name: "multiple URLs",
    input: "Try https://a.com and https://b.com too",
    expected: [
      { type: "text", content: "Try " },
      { type: "link", content: "https://a.com", href: "https://a.com" },
      { type: "text", content: " and " },
      { type: "link", content: "https://b.com", href: "https://b.com" },
      { type: "text", content: " too" },
    ],
  },
  {
    name: "URL at start of text",
    input: "https://example.com is the site",
    expected: [
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: " is the site" },
    ],
  },
  {
    name: "URL at end of text",
    input: "The site is https://example.com",
    expected: [
      { type: "text", content: "The site is " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
    ],
  },
  {
    name: "URL only",
    input: "https://example.com",
    expected: [{ type: "link", content: "https://example.com", href: "https://example.com" }],
  },
  {
    name: "empty string",
    input: "",
    expected: [],
  },
  {
    name: "URL with fragment",
    input: "See https://example.com/page#section for more",
    expected: [
      { type: "text", content: "See " },
      {
        type: "link",
        content: "https://example.com/page#section",
        href: "https://example.com/page#section",
      },
      { type: "text", content: " for more" },
    ],
  },
  {
    name: "URL in parentheses - should not include closing paren",
    input: "(see https://example.com)",
    expected: [
      { type: "text", content: "(see " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ")" },
    ],
  },
  {
    name: "URL with trailing colon and more text",
    input: "URL: https://example.com: that was it",
    expected: [
      { type: "text", content: "URL: " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: ": that was it" },
    ],
  },
  {
    name: "does not match ftp URLs",
    input: "Not matched: ftp://example.com",
    expected: [{ type: "text", content: "Not matched: ftp://example.com" }],
  },
  {
    name: "does not match mailto",
    input: "Email: mailto:test@example.com",
    expected: [{ type: "text", content: "Email: mailto:test@example.com" }],
  },
  {
    name: "URL with underscores and dashes",
    input: "Go to https://my-site.example.com/some_page",
    expected: [
      { type: "text", content: "Go to " },
      {
        type: "link",
        content: "https://my-site.example.com/some_page",
        href: "https://my-site.example.com/some_page",
      },
    ],
  },
  {
    name: "URL followed by semicolon",
    input: "First https://a.com; then more",
    expected: [
      { type: "text", content: "First " },
      { type: "link", content: "https://a.com", href: "https://a.com" },
      { type: "text", content: "; then more" },
    ],
  },
  {
    name: "newlines around URL",
    input: "Line 1\nhttps://example.com\nLine 3",
    expected: [
      { type: "text", content: "Line 1\n" },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "\nLine 3" },
    ],
  },
  {
    name: "XSS attempt in URL - javascript protocol not matched",
    input: "javascript:alert('xss')",
    expected: [{ type: "text", content: "javascript:alert('xss')" }],
  },
  {
    name: "XSS attempt - script tags in text preserved as text",
    input: "<script>alert('xss')</script> https://example.com",
    expected: [
      { type: "text", content: "<script>alert('xss')</script> " },
      { type: "link", content: "https://example.com", href: "https://example.com" },
    ],
  },
  {
    name: "URL not matched inside angle brackets",
    input: "See <https://example.com> for more",
    expected: [
      { type: "text", content: "See <" },
      { type: "link", content: "https://example.com", href: "https://example.com" },
      { type: "text", content: "> for more" },
    ],
  },
  {
    name: "URL in markdown bold - should not include asterisks",
    input: "Download here: **https://example.com/file.vsix**",
    expected: [
      { type: "text", content: "Download here: **" },
      {
        type: "link",
        content: "https://example.com/file.vsix",
        href: "https://example.com/file.vsix",
      },
      { type: "text", content: "**" },
    ],
  },
];

function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== typeof b) return false;
  if (a === null || b === null) return a === b;
  if (typeof a !== "object") return false;

  if (Array.isArray(a) && Array.isArray(b)) {
    if (a.length !== b.length) return false;
    return a.every((item, i) => deepEqual(item, b[i]));
  }

  if (Array.isArray(a) || Array.isArray(b)) return false;

  const aObj = a as Record<string, unknown>;
  const bObj = b as Record<string, unknown>;
  const aKeys = Object.keys(aObj);
  const bKeys = Object.keys(bObj);

  if (aKeys.length !== bKeys.length) return false;
  return aKeys.every((key) => deepEqual(aObj[key], bObj[key]));
}

export function runTests(): { passed: number; failed: number; failures: string[] } {
  let passed = 0;
  let failed = 0;
  const failures: string[] = [];

  for (const tc of testCases) {
    const result = parseLinks(tc.input);
    if (deepEqual(result, tc.expected)) {
      passed++;
    } else {
      failed++;
      failures.push(
        `FAIL: ${tc.name}\n  Input: ${JSON.stringify(tc.input)}\n  Expected: ${JSON.stringify(tc.expected)}\n  Got: ${JSON.stringify(result)}`,
      );
    }
  }

  return { passed, failed, failures };
}

// Export test cases for use in browser
export { testCases };
