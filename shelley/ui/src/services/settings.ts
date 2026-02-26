const MARKDOWN_KEY = "shelley-markdown-rendering";

export type MarkdownMode = "off" | "agent" | "all";

export function getMarkdownMode(): MarkdownMode {
  const val = localStorage.getItem(MARKDOWN_KEY);
  // Migrate old boolean values
  if (val === "true") return "agent";
  if (val === "false") return "off";
  if (val === "agent" || val === "all" || val === "off") return val;
  return "agent"; // default
}

export function setMarkdownMode(mode: MarkdownMode): void {
  localStorage.setItem(MARKDOWN_KEY, mode);
}
