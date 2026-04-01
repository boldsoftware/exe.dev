import React, { useMemo } from "react";
import { ansiToHtml } from "../utils/ansi";

interface AnsiTextProps {
  text: string;
  className?: string;
}

/**
 * Renders text that may contain ANSI escape sequences.
 * If ANSI codes are detected, renders as sanitized HTML with inline styles.
 * Otherwise renders as plain text (no dangerouslySetInnerHTML overhead).
 */
const AnsiText = React.forwardRef<HTMLPreElement, AnsiTextProps>(({ text, className }, ref) => {
  const html = useMemo(() => ansiToHtml(text), [text]);

  if (html) {
    return <pre ref={ref} className={className} dangerouslySetInnerHTML={{ __html: html }} />;
  }

  return (
    <pre ref={ref} className={className}>
      {text}
    </pre>
  );
});

AnsiText.displayName = "AnsiText";
export default AnsiText;
