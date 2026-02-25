import React, { createContext, useContext, useState, useCallback } from "react";
import { getMarkdownEnabled, setMarkdownEnabled } from "../services/settings";

interface MarkdownContextType {
  markdownEnabled: boolean;
  toggleMarkdown: () => void;
}

const MarkdownContext = createContext<MarkdownContextType>({
  markdownEnabled: true,
  toggleMarkdown: () => {},
});

export function MarkdownProvider({ children }: { children: React.ReactNode }) {
  const [markdownEnabled, setEnabled] = useState(getMarkdownEnabled);

  const toggleMarkdown = useCallback(() => {
    setEnabled((prev) => {
      const next = !prev;
      setMarkdownEnabled(next);
      return next;
    });
  }, []);

  return (
    <MarkdownContext.Provider value={{ markdownEnabled, toggleMarkdown }}>
      {children}
    </MarkdownContext.Provider>
  );
}

export function useMarkdown() {
  return useContext(MarkdownContext);
}
