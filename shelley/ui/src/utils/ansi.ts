import Convert from "ansi-to-html";
import DOMPurify from "dompurify";

// Comprehensive regex matching all common ANSI escape sequences:
// CSI sequences (\x1b[...), OSC sequences (\x1b]...\x07 or \x1b]...\x1b\\),
// and simple two-byte escapes (\x1b followed by a single char).
/* eslint-disable no-control-regex */
const ANSI_RE =
  /[\x1b\x9b][[\]()#;?]*(?:(?:[0-9]{1,4}(?:;[0-9]{0,4})*)?[0-9A-ORZcf-nqry=><~]|\x07|\x1b\\)/g;
/* eslint-enable no-control-regex */

/**
 * Converts a string containing ANSI escape sequences into sanitized HTML.
 * Returns the empty string if no escape sequences are present.
 */
export function ansiToHtml(text: string): string {
  // Fast path: no escape sequences present
  // eslint-disable-next-line no-control-regex
  if (!/[\x1b\x9b]/.test(text)) {
    return "";
  }
  // Create a fresh converter each call to avoid state leaking between calls.
  const converter = new Convert({
    escapeXML: true,
    newline: false,
    stream: false,
  });
  const raw = converter.toHtml(text);
  return DOMPurify.sanitize(raw, {
    ALLOWED_TAGS: ["span", "br", "b", "i", "u"],
    ALLOWED_ATTR: ["style"],
  });
}

/**
 * Strips all ANSI escape sequences from a string.
 */
export function stripAnsi(text: string): string {
  return text.replace(ANSI_RE, "");
}
