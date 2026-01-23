import React, { useState, useEffect, useRef, useCallback } from "react";
import type * as Monaco from "monaco-editor";
import { LLMContent } from "../types";
import { isDarkModeActive } from "../services/theme";

// LocalStorage keys for preferences
const STORAGE_KEY_MONACO_ENABLED = "shelley-use-monaco-diff";
const STORAGE_KEY_SIDE_BY_SIDE = "shelley-diff-side-by-side";

// Feature flag for Monaco diff view
function useMonacoDiff(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY_MONACO_ENABLED) === "true";
  } catch {
    return false;
  }
}

// Get saved side-by-side preference (default: true for desktop)
function getSideBySidePreference(): boolean {
  try {
    const stored = localStorage.getItem(STORAGE_KEY_SIDE_BY_SIDE);
    if (stored !== null) {
      return stored === "true";
    }
    // Default to side-by-side on desktop, inline on mobile
    return window.innerWidth >= 768;
  } catch {
    return window.innerWidth >= 768;
  }
}

function setSideBySidePreference(value: boolean): void {
  try {
    localStorage.setItem(STORAGE_KEY_SIDE_BY_SIDE, value ? "true" : "false");
  } catch {
    // Ignore storage errors
  }
}

// Display data structure from the patch tool
interface PatchDisplayData {
  path: string;
  oldContent: string;
  newContent: string;
  diff: string;
}

interface PatchToolProps {
  // For tool_use (pending state)
  toolInput?: unknown;
  isRunning?: boolean;

  // For tool_result (completed state)
  toolResult?: LLMContent[];
  hasError?: boolean;
  executionTime?: string;
  display?: unknown; // Display data from the tool_result Content (contains the diff or structured data)
  onCommentTextChange?: (text: string) => void;
}

// Global Monaco instance - loaded lazily
let monacoInstance: typeof Monaco | null = null;
let monacoLoadPromise: Promise<typeof Monaco> | null = null;

function loadMonaco(): Promise<typeof Monaco> {
  if (monacoInstance) {
    return Promise.resolve(monacoInstance);
  }
  if (monacoLoadPromise) {
    return monacoLoadPromise;
  }

  monacoLoadPromise = (async () => {
    // Configure Monaco environment for web workers before importing
    const monacoEnv: Monaco.Environment = {
      getWorkerUrl: () => "/editor.worker.js",
    };
    (self as Window).MonacoEnvironment = monacoEnv;

    // Load Monaco CSS if not already loaded
    if (!document.querySelector('link[href="/monaco-editor.css"]')) {
      const link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = "/monaco-editor.css";
      document.head.appendChild(link);
    }

    // Load Monaco from our local bundle (runtime URL, cast to proper types)
    // eslint-disable-next-line @typescript-eslint/ban-ts-comment
    // @ts-ignore - dynamic runtime URL import
    const monaco = (await import("/monaco-editor.js")) as typeof Monaco;
    monacoInstance = monaco;
    return monacoInstance;
  })();

  return monacoLoadPromise;
}

// Simple diff view component (default)
function SimpleDiffView({ displayData }: { displayData: PatchDisplayData | null }) {
  // Get diff text from displayData or fall back to empty
  const diff = displayData?.diff || "";

  // Parse unified diff to extract lines
  const lines = diff ? diff.split("\n") : [];

  return (
    <pre className="patch-tool-diff">
      {lines.map((line, idx) => {
        // Determine line type for styling
        let className = "patch-diff-line";
        if (line.startsWith("+") && !line.startsWith("+++")) {
          className += " patch-diff-addition";
        } else if (line.startsWith("-") && !line.startsWith("---")) {
          className += " patch-diff-deletion";
        } else if (line.startsWith("@@")) {
          className += " patch-diff-hunk";
        } else if (line.startsWith("---") || line.startsWith("+++")) {
          className += " patch-diff-header";
        }

        return (
          <div key={idx} className={className}>
            {line || " "}
          </div>
        );
      })}
    </pre>
  );
}

// Monaco diff view component (feature-flagged)
function MonacoDiffView({
  displayData,
  isMobile,
  sideBySide,
  onCommentTextChange,
  filename,
}: {
  displayData: PatchDisplayData;
  isMobile: boolean;
  sideBySide: boolean;
  onCommentTextChange?: (text: string) => void;
  filename: string;
}) {
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [isVisible, setIsVisible] = useState(false);
  const [editorHeight, setEditorHeight] = useState<number>(100);
  const [showCommentDialog, setShowCommentDialog] = useState<{
    line: number;
    selectedText?: string;
  } | null>(null);
  const [commentText, setCommentText] = useState("");

  const containerRef = useRef<HTMLDivElement>(null);
  const editorContainerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneDiffEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const commentInputRef = useRef<HTMLTextAreaElement>(null);
  const hoverDecorationsRef = useRef<string[]>([]);
  const heightSetRef = useRef(false);
  const modelsRef = useRef<{
    original: Monaco.editor.ITextModel | null;
    modified: Monaco.editor.ITextModel | null;
  }>({
    original: null,
    modified: null,
  });

  // Intersection observer for lazy loading
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setIsVisible(true);
            // Once visible, we don't need to observe anymore
            observer.disconnect();
          }
        }
      },
      {
        rootMargin: "100px", // Start loading a bit before it's visible
        threshold: 0,
      },
    );

    observer.observe(container);

    return () => observer.disconnect();
  }, []);

  // Load Monaco only when visible
  useEffect(() => {
    if (!isVisible || monacoLoaded) return;

    loadMonaco()
      .then((monaco) => {
        monacoRef.current = monaco;
        setMonacoLoaded(true);
      })
      .catch((err) => {
        console.error("Failed to load Monaco:", err);
      });
  }, [isVisible, monacoLoaded]);

  // Update side-by-side mode when prop changes
  useEffect(() => {
    if (editorRef.current) {
      editorRef.current.updateOptions({ renderSideBySide: sideBySide });
      // Reset height flag to allow recalculation after mode change
      heightSetRef.current = false;
    }
  }, [sideBySide]);

  // Create Monaco editor when data is ready and visible
  useEffect(() => {
    if (!monacoLoaded || !isVisible || !editorContainerRef.current || !monacoRef.current) {
      return;
    }

    const monaco = monacoRef.current;

    // Dispose previous editor and models
    if (editorRef.current) {
      editorRef.current.dispose();
      editorRef.current = null;
    }
    if (modelsRef.current.original) {
      modelsRef.current.original.dispose();
      modelsRef.current.original = null;
    }
    if (modelsRef.current.modified) {
      modelsRef.current.modified.dispose();
      modelsRef.current.modified = null;
    }

    // Reset height tracking for new editor
    heightSetRef.current = false;

    // Get language from file extension
    const ext = "." + (displayData.path.split(".").pop()?.toLowerCase() || "");
    const languages = monaco.languages.getLanguages();
    let language = "plaintext";
    for (const lang of languages) {
      if (lang.extensions?.includes(ext)) {
        language = lang.id;
        break;
      }
    }

    // Create models with unique URIs (include timestamp to avoid conflicts)
    const timestamp = Date.now();
    const originalUri = monaco.Uri.file(`patch-original-${timestamp}-${displayData.path}`);
    const modifiedUri = monaco.Uri.file(`patch-modified-${timestamp}-${displayData.path}`);

    // Check for and dispose any existing models with these URIs (defensive, shouldn't happen)
    const existingOriginal = monaco.editor.getModel(originalUri);
    if (existingOriginal) existingOriginal.dispose();
    const existingModified = monaco.editor.getModel(modifiedUri);
    if (existingModified) existingModified.dispose();

    const originalModel = monaco.editor.createModel(displayData.oldContent, language, originalUri);
    const modifiedModel = monaco.editor.createModel(displayData.newContent, language, modifiedUri);
    modelsRef.current = { original: originalModel, modified: modifiedModel };

    // Create diff editor with collapsed unchanged regions
    const diffEditor = monaco.editor.createDiffEditor(editorContainerRef.current, {
      theme: isDarkModeActive() ? "vs-dark" : "vs",
      readOnly: true,
      originalEditable: false,
      automaticLayout: true,
      renderSideBySide: sideBySide,
      enableSplitViewResizing: true,
      renderIndicators: true,
      renderMarginRevertIcon: false,
      lineNumbers: isMobile ? "off" : "on",
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      wordWrap: "on",
      glyphMargin: !isMobile, // Enable glyph margin for comment indicator
      lineDecorationsWidth: isMobile ? 0 : 10,
      lineNumbersMinChars: isMobile ? 0 : 3,
      quickSuggestions: false,
      suggestOnTriggerCharacters: false,
      lightbulb: { enabled: false },
      codeLens: false,
      contextmenu: false,
      links: false,
      folding: !isMobile,
      // Hide unchanged regions to show only edited sections
      hideUnchangedRegions: {
        enabled: true,
        revealLineCount: 2, // Show 2 lines of context around changes
        minimumLineCount: 3, // Hide regions with 3+ unchanged lines
        contextLineCount: 2, // Context lines to show when expanding
      },
      // Disable scrollbar when content fits
      scrollbar: {
        vertical: "auto",
        horizontal: "auto",
        alwaysConsumeMouseWheel: false,
      },
    });

    diffEditor.setModel({
      original: originalModel,
      modified: modifiedModel,
    });

    editorRef.current = diffEditor;

    // Function to update height - only do this once to avoid scroll disruption
    const updateHeight = () => {
      if (heightSetRef.current) return;

      const modifiedEditor = diffEditor.getModifiedEditor();
      const contentHeight = modifiedEditor.getContentHeight();

      if (contentHeight > 0) {
        // Add small buffer, no max height - let it expand fully
        const newHeight = Math.max(60, contentHeight + 4);
        heightSetRef.current = true;
        setEditorHeight(newHeight);
      }
    };

    // Update height after diff is computed
    // Monaco needs time to compute the diff and layout
    const heightUpdateTimer = setTimeout(updateHeight, 200);

    // Also listen for content size change (fires when diff is computed)
    const modifiedEditor = diffEditor.getModifiedEditor();
    const contentSizeDisposable = modifiedEditor.onDidContentSizeChange(() => {
      updateHeight();
    });

    // Add click handler for commenting if callback is provided
    if (onCommentTextChange) {
      const openCommentDialog = (lineNumber: number) => {
        const model = modifiedEditor.getModel();
        const selection = modifiedEditor.getSelection();
        let selectedText = "";

        if (selection && !selection.isEmpty() && model) {
          selectedText = model.getValueInRange(selection);
        } else if (model) {
          selectedText = model.getLineContent(lineNumber) || "";
        }

        setShowCommentDialog({
          line: lineNumber,
          selectedText,
        });
      };

      modifiedEditor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
        const isLineClick =
          e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
          e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;

        if (isLineClick) {
          const position = e.target.position;
          if (position) {
            openCommentDialog(position.lineNumber);
          }
        }
      });

      // Add hover highlighting with comment indicator
      let lastHoveredLine = -1;
      modifiedEditor.onMouseMove((e: Monaco.editor.IEditorMouseEvent) => {
        const position = e.target.position;
        const lineNumber = position?.lineNumber ?? -1;

        if (lineNumber === lastHoveredLine) return;
        lastHoveredLine = lineNumber;

        if (lineNumber > 0) {
          hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
            hoverDecorationsRef.current,
            [
              {
                range: new monaco.Range(lineNumber, 1, lineNumber, 1),
                options: {
                  isWholeLine: true,
                  className: "patch-line-hover",
                  glyphMarginClassName: "patch-comment-glyph",
                },
              },
            ],
          );
        } else {
          // Clear decorations when not hovering a line
          hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
            hoverDecorationsRef.current,
            [],
          );
        }
      });

      // Clear decorations when mouse leaves editor
      modifiedEditor.onMouseLeave(() => {
        lastHoveredLine = -1;
        hoverDecorationsRef.current = modifiedEditor.deltaDecorations(
          hoverDecorationsRef.current,
          [],
        );
      });
    }

    // Cleanup function
    return () => {
      clearTimeout(heightUpdateTimer);
      contentSizeDisposable.dispose();
      if (editorRef.current) {
        editorRef.current.dispose();
        editorRef.current = null;
      }
      if (modelsRef.current.original) {
        modelsRef.current.original.dispose();
        modelsRef.current.original = null;
      }
      if (modelsRef.current.modified) {
        modelsRef.current.modified.dispose();
        modelsRef.current.modified = null;
      }
    };
  }, [monacoLoaded, isVisible, displayData, isMobile, onCommentTextChange, sideBySide]);

  // Update Monaco theme when dark mode changes
  useEffect(() => {
    if (!monacoRef.current) return;

    const updateMonacoTheme = () => {
      const theme = isDarkModeActive() ? "vs-dark" : "vs";
      monacoRef.current?.editor.setTheme(theme);
    };

    const observer = new MutationObserver((mutations) => {
      for (const mutation of mutations) {
        if (mutation.attributeName === "class") {
          updateMonacoTheme();
        }
      }
    });

    observer.observe(document.documentElement, { attributes: true });

    return () => observer.disconnect();
  }, [monacoLoaded]);

  // Focus comment input when dialog opens
  useEffect(() => {
    if (showCommentDialog && commentInputRef.current) {
      setTimeout(() => {
        commentInputRef.current?.focus();
      }, 50);
    }
  }, [showCommentDialog]);

  // Handle adding a comment
  const handleAddComment = useCallback(() => {
    if (!showCommentDialog || !commentText.trim() || !onCommentTextChange) return;

    const line = showCommentDialog.line;
    const codeSnippet = showCommentDialog.selectedText?.split("\n")[0]?.trim() || "";
    const truncatedCode =
      codeSnippet.length > 60 ? codeSnippet.substring(0, 57) + "..." : codeSnippet;

    const commentBlock = `> ${filename}:${line}: ${truncatedCode}\n${commentText}\n\n`;

    onCommentTextChange(commentBlock);
    setShowCommentDialog(null);
    setCommentText("");
  }, [showCommentDialog, commentText, onCommentTextChange, filename]);

  return (
    <div ref={containerRef} className="patch-tool-monaco-container">
      {/* Monaco editor container */}
      {!isVisible ? (
        <div className="patch-tool-monaco-placeholder" style={{ height: "100px" }}>
          <span>Scroll to load diff...</span>
        </div>
      ) : !monacoLoaded ? (
        <div className="patch-tool-monaco-placeholder" style={{ height: "100px" }}>
          <div className="spinner-small" />
          <span>Loading editor...</span>
        </div>
      ) : (
        <div
          ref={editorContainerRef}
          className="patch-tool-monaco-editor"
          style={{ height: `${editorHeight}px`, width: "100%" }}
        />
      )}

      {/* Comment dialog */}
      {showCommentDialog && onCommentTextChange && (
        <div className="patch-tool-comment-dialog">
          <h4>Add Comment (Line {showCommentDialog.line})</h4>
          {showCommentDialog.selectedText && (
            <pre className="patch-tool-selected-text">{showCommentDialog.selectedText}</pre>
          )}
          <textarea
            ref={commentInputRef}
            value={commentText}
            onChange={(e) => setCommentText(e.target.value)}
            placeholder="Enter your comment..."
            className="patch-tool-comment-input"
            autoFocus
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setShowCommentDialog(null);
              } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
                handleAddComment();
              }
            }}
          />
          <div className="patch-tool-comment-actions">
            <button
              onClick={() => setShowCommentDialog(null)}
              className="patch-tool-btn patch-tool-btn-secondary"
            >
              Cancel
            </button>
            <button
              onClick={handleAddComment}
              className="patch-tool-btn patch-tool-btn-primary"
              disabled={!commentText.trim()}
            >
              Add Comment
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// Side-by-side toggle icon component
function DiffModeToggle({ sideBySide, onToggle }: { sideBySide: boolean; onToggle: () => void }) {
  return (
    <button
      className="patch-tool-diff-mode-toggle"
      onClick={(e) => {
        e.stopPropagation();
        onToggle();
      }}
      title={sideBySide ? "Switch to inline diff" : "Switch to side-by-side diff"}
    >
      <svg
        width="14"
        height="14"
        viewBox="0 0 14 14"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        {sideBySide ? (
          // Side-by-side icon (two columns)
          <>
            <rect
              x="1"
              y="2"
              width="5"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
            <rect
              x="8"
              y="2"
              width="5"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
          </>
        ) : (
          // Inline icon (single column with horizontal lines)
          <>
            <rect
              x="2"
              y="2"
              width="10"
              height="10"
              rx="1"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
            <line x1="4" y1="5" x2="10" y2="5" stroke="currentColor" strokeWidth="1.5" />
            <line x1="4" y1="9" x2="10" y2="9" stroke="currentColor" strokeWidth="1.5" />
          </>
        )}
      </svg>
    </button>
  );
}

function PatchTool({
  toolInput,
  isRunning,
  toolResult,
  hasError,
  display,
  onCommentTextChange,
}: PatchToolProps) {
  // Default to collapsed for errors (since agents typically recover), expanded otherwise
  const [isExpanded, setIsExpanded] = useState(!hasError);
  const [isMobile, setIsMobile] = useState(window.innerWidth < 768);
  const [sideBySide, setSideBySide] = useState(() => !isMobile && getSideBySidePreference());

  // Check feature flag for Monaco diff view
  const useMonaco = useMonacoDiff();

  // Track viewport size
  useEffect(() => {
    const handleResize = () => {
      setIsMobile(window.innerWidth < 768);
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  // Toggle side-by-side mode
  const toggleSideBySide = useCallback(() => {
    const newValue = !sideBySide;
    setSideBySide(newValue);
    setSideBySidePreference(newValue);
  }, [sideBySide]);

  // Extract path from toolInput
  const path =
    typeof toolInput === "object" &&
    toolInput !== null &&
    "path" in toolInput &&
    typeof toolInput.path === "string"
      ? toolInput.path
      : typeof toolInput === "string"
        ? toolInput
        : "";

  // Parse display data (structured format from backend)
  const displayData: PatchDisplayData | null =
    display &&
    typeof display === "object" &&
    "path" in display &&
    "oldContent" in display &&
    "newContent" in display
      ? (display as PatchDisplayData)
      : null;

  // Extract error message from toolResult if present
  const errorMessage =
    toolResult && toolResult.length > 0 && toolResult[0].Text ? toolResult[0].Text : "";

  const isComplete = !isRunning && toolResult !== undefined;

  // Extract filename from path or diff headers
  const filename = displayData?.path || path || "patch";

  // Show toggle only for Monaco view on desktop when expanded and complete
  const showDiffToggle =
    useMonaco && !isMobile && isExpanded && isComplete && !hasError && displayData;

  return (
    <div
      className="patch-tool"
      data-testid={isComplete ? "tool-call-completed" : "tool-call-running"}
    >
      <div className="patch-tool-header" onClick={() => setIsExpanded(!isExpanded)}>
        <div className="patch-tool-summary">
          <span className={`patch-tool-emoji ${isRunning ? "running" : ""}`}>🖋️</span>
          <span className="patch-tool-filename">{filename}</span>
          {isComplete && hasError && <span className="patch-tool-error">✗</span>}
          {isComplete && !hasError && <span className="patch-tool-success">✓</span>}
        </div>
        <div className="patch-tool-header-controls">
          {showDiffToggle && <DiffModeToggle sideBySide={sideBySide} onToggle={toggleSideBySide} />}
          <button
            className="patch-tool-toggle"
            aria-label={isExpanded ? "Collapse" : "Expand"}
            aria-expanded={isExpanded}
          >
            <svg
              width="12"
              height="12"
              viewBox="0 0 12 12"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
              style={{
                transform: isExpanded ? "rotate(90deg)" : "rotate(0deg)",
                transition: "transform 0.2s",
              }}
            >
              <path
                d="M4.5 3L7.5 6L4.5 9"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        </div>
      </div>

      {isExpanded && (
        <div className="patch-tool-details">
          {isComplete && !hasError && displayData && (
            <div className="patch-tool-section">
              {useMonaco ? (
                <MonacoDiffView
                  displayData={displayData}
                  isMobile={isMobile}
                  sideBySide={sideBySide}
                  onCommentTextChange={onCommentTextChange}
                  filename={filename}
                />
              ) : (
                <SimpleDiffView displayData={displayData} />
              )}
            </div>
          )}

          {isComplete && hasError && (
            <div className="patch-tool-section">
              <pre className="patch-tool-error-message">{errorMessage || "Patch failed"}</pre>
            </div>
          )}

          {isRunning && (
            <div className="patch-tool-section">
              <div className="patch-tool-label">Applying patch...</div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export default PatchTool;
