const MARKDOWN_KEY = "shelley-markdown-rendering";

export function getMarkdownEnabled(): boolean {
  const val = localStorage.getItem(MARKDOWN_KEY);
  if (val === null) return true;
  return val === "true";
}

export function setMarkdownEnabled(enabled: boolean): void {
  localStorage.setItem(MARKDOWN_KEY, enabled ? "true" : "false");
}
