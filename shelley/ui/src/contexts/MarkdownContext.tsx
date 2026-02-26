import React, { createContext, useContext, useState, useCallback } from "react";
import {
  type MarkdownMode,
  getMarkdownMode,
  setMarkdownMode as persistMode,
} from "../services/settings";

interface MarkdownContextType {
  markdownMode: MarkdownMode;
  setMarkdownMode: (mode: MarkdownMode) => void;
}

const MarkdownContext = createContext<MarkdownContextType>({
  markdownMode: "agent",
  setMarkdownMode: () => {},
});

export function MarkdownProvider({ children }: { children: React.ReactNode }) {
  const [mode, setMode] = useState<MarkdownMode>(getMarkdownMode);

  const updateMode = useCallback((m: MarkdownMode) => {
    persistMode(m);
    setMode(m);
  }, []);

  return (
    <MarkdownContext.Provider value={{ markdownMode: mode, setMarkdownMode: updateMode }}>
      {children}
    </MarkdownContext.Provider>
  );
}

export function useMarkdown() {
  return useContext(MarkdownContext);
}
