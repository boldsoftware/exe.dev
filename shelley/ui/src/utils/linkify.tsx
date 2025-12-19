import React from "react";

// Regex for matching URLs. Only matches http:// and https:// URLs.
// Avoids matching trailing punctuation that's likely not part of the URL.
// eslint-disable-next-line no-useless-escape
const URL_REGEX = /https?:\/\/[^\s<>"'`\]\)]+[^\s<>"'`\]\).,:;!?]/g;

export interface LinkifyResult {
  type: "text" | "link";
  content: string;
  href?: string;
}

/**
 * Parse text and extract URLs as separate segments.
 * Returns an array of text and link segments.
 */
export function parseLinks(text: string): LinkifyResult[] {
  const results: LinkifyResult[] = [];
  let lastIndex = 0;

  // Reset regex state
  URL_REGEX.lastIndex = 0;

  let match;
  while ((match = URL_REGEX.exec(text)) !== null) {
    // Add text before the match
    if (match.index > lastIndex) {
      results.push({
        type: "text",
        content: text.slice(lastIndex, match.index),
      });
    }

    // Add the link
    const url = match[0];
    results.push({
      type: "link",
      content: url,
      href: url,
    });

    lastIndex = match.index + url.length;
  }

  // Add remaining text after last match
  if (lastIndex < text.length) {
    results.push({
      type: "text",
      content: text.slice(lastIndex),
    });
  }

  return results;
}

/**
 * Convert text containing URLs into React elements with clickable links.
 * URLs are rendered as <a> tags that open in new tabs.
 * Text is HTML-escaped by React's default behavior.
 */
export function linkifyText(text: string): React.ReactNode {
  const segments = parseLinks(text);

  if (segments.length === 0) {
    return text;
  }

  // If there's only one text segment with no links, return plain text
  if (segments.length === 1 && segments[0].type === "text") {
    return text;
  }

  return segments.map((segment, index) => {
    if (segment.type === "link") {
      return (
        <a
          key={index}
          href={segment.href}
          target="_blank"
          rel="noopener noreferrer"
          className="text-link"
        >
          {segment.content}
        </a>
      );
    }
    return <React.Fragment key={index}>{segment.content}</React.Fragment>;
  });
}
