// Build a markdown blockquote of the user's selected snippet. The trailing
// blank line puts the composer cursor below the quote. The messageId is
// currently unused, but kept in the signature so callers can later include
// a stable reference if needed.
export function buildMessageQuote(_messageId: string, snippet: string): string {
  const trimmed = snippet.replace(/\s+$/, "");
  if (!trimmed) return "";
  const quoted = trimmed
    .split("\n")
    .map((line) => `> ${line}`)
    .join("\n");
  return `${quoted}\n\n`;
}
