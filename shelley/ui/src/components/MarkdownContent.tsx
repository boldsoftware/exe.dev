import React, { useMemo } from "react";
import { Marked } from "marked";
import DOMPurify from "dompurify";

interface MarkdownContentProps {
  text: string;
}

// Create a dedicated marked instance to avoid mutating the global singleton
const markedInstance = new Marked({
  gfm: true,
  breaks: true,
});

// Make all links open in new tabs, and restrict <input> to checkboxes only.
DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("target", "_blank");
    node.setAttribute("rel", "noopener noreferrer");
  }
  // Only allow checkbox inputs (for GFM task lists); remove all others.
  if (node.tagName === "INPUT" && node.getAttribute("type") !== "checkbox") {
    node.remove();
  }
});

function MarkdownContent({ text }: MarkdownContentProps) {
  const html = useMemo(() => {
    const raw = markedInstance.parse(text, { async: false }) as string;
    // img intentionally excluded — we don't render arbitrary remote images from agent output
    return DOMPurify.sanitize(raw, {
      ALLOWED_TAGS: [
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
      ],
      ALLOWED_ATTR: ["href", "target", "rel", "type", "checked", "disabled", "class"],
    });
  }, [text]);

  return (
    <div className="markdown-content break-words" dangerouslySetInnerHTML={{ __html: html }} />
  );
}

export default MarkdownContent;
