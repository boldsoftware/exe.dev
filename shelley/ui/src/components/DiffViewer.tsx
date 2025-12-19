import React, { useState, useEffect, useCallback, useRef } from "react";
import type * as Monaco from "monaco-editor";
import { api } from "../services/api";
import { GitDiffInfo, GitFileInfo, GitFileDiff } from "../types";

interface DiffViewerProps {
  cwd: string;
  isOpen: boolean;
  onClose: () => void;
  onCommentTextChange: (text: string) => void;
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

type ViewMode = "comment" | "edit";

function DiffViewer({ cwd, isOpen, onClose, onCommentTextChange }: DiffViewerProps) {
  const [diffs, setDiffs] = useState<GitDiffInfo[]>([]);
  const [gitRoot, setGitRoot] = useState<string | null>(null);
  const [selectedDiff, setSelectedDiff] = useState<string | null>(null);
  const [files, setFiles] = useState<GitFileInfo[]>([]);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileDiff, setFileDiff] = useState<GitFileDiff | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [currentChangeIndex, setCurrentChangeIndex] = useState<number>(-1);
  const [saveStatus, setSaveStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const saveTimeoutRef = useRef<number | null>(null);
  const pendingSaveRef = useRef<(() => Promise<void>) | null>(null);
  const scheduleSaveRef = useRef<(() => void) | null>(null);
  const contentChangeDisposableRef = useRef<Monaco.IDisposable | null>(null);
  const [showCommentDialog, setShowCommentDialog] = useState<{
    line: number;
    side: "left" | "right";
    selectedText?: string;
    startLine?: number;
    endLine?: number;
  } | null>(null);
  const [commentText, setCommentText] = useState("");
  const [mode, setMode] = useState<ViewMode>("comment");
  const [selectorsExpanded, setSelectorsExpanded] = useState(false);
  const [isMobile, setIsMobile] = useState(window.innerWidth < 768);
  const editorContainerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<Monaco.editor.IStandaloneDiffEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const modeRef = useRef<ViewMode>(mode);

  // Keep modeRef in sync with mode state and update editor options
  useEffect(() => {
    modeRef.current = mode;
    // Update editor readOnly state when mode changes
    if (editorRef.current) {
      const modifiedEditor = editorRef.current.getModifiedEditor();
      modifiedEditor.updateOptions({ readOnly: mode === "comment" });
    }
  }, [mode]);

  // Track viewport size
  useEffect(() => {
    const handleResize = () => {
      setIsMobile(window.innerWidth < 768);
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  // Load Monaco when viewer opens
  useEffect(() => {
    if (isOpen && !monacoLoaded) {
      loadMonaco()
        .then((monaco) => {
          monacoRef.current = monaco;
          setMonacoLoaded(true);
        })
        .catch((err) => {
          console.error("Failed to load Monaco:", err);
          setError("Failed to load diff editor");
        });
    }
  }, [isOpen, monacoLoaded]);

  // Load diffs when viewer opens
  useEffect(() => {
    if (isOpen && cwd) {
      loadDiffs();
    }
  }, [isOpen, cwd]);

  // Load files when diff is selected
  useEffect(() => {
    if (selectedDiff && cwd) {
      loadFiles(selectedDiff);
    }
  }, [selectedDiff, cwd]);

  // Load file diff when file is selected
  useEffect(() => {
    if (selectedDiff && selectedFile && cwd) {
      loadFileDiff(selectedDiff, selectedFile);
      setCurrentChangeIndex(-1); // Reset change index for new file
    }
  }, [selectedDiff, selectedFile, cwd]);

  // Create/update Monaco editor when fileDiff changes
  useEffect(() => {
    if (!monacoLoaded || !fileDiff || !editorContainerRef.current || !monacoRef.current) {
      return;
    }

    const monaco = monacoRef.current;

    // Dispose previous editor
    if (editorRef.current) {
      editorRef.current.dispose();
      editorRef.current = null;
    }

    // Get language from file extension
    const ext = "." + (fileDiff.path.split(".").pop()?.toLowerCase() || "");
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
    const originalUri = monaco.Uri.file(`original-${timestamp}-${fileDiff.path}`);
    const modifiedUri = monaco.Uri.file(`modified-${timestamp}-${fileDiff.path}`);

    const originalModel = monaco.editor.createModel(fileDiff.oldContent, language, originalUri);
    const modifiedModel = monaco.editor.createModel(fileDiff.newContent, language, modifiedUri);

    // Create diff editor with mobile-friendly options
    const diffEditor = monaco.editor.createDiffEditor(editorContainerRef.current, {
      theme: "vs",
      readOnly: true, // Always read-only in diff viewer
      originalEditable: false,
      automaticLayout: true,
      renderSideBySide: !isMobile,
      enableSplitViewResizing: true,
      renderIndicators: true,
      renderMarginRevertIcon: false,
      lineNumbers: isMobile ? "off" : "on",
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      wordWrap: "on",
      glyphMargin: false, // No glyph margin - click on lines to comment
      lineDecorationsWidth: isMobile ? 0 : 10,
      lineNumbersMinChars: isMobile ? 0 : 3,
      quickSuggestions: false,
      suggestOnTriggerCharacters: false,
      lightbulb: { enabled: false },
      codeLens: false,
      contextmenu: false,
      links: false,
      folding: !isMobile,
    });

    diffEditor.setModel({
      original: originalModel,
      modified: modifiedModel,
    });

    editorRef.current = diffEditor;

    // Add click handler for commenting - clicking on a line in comment mode opens dialog
    const modifiedEditor = diffEditor.getModifiedEditor();
    modifiedEditor.onMouseDown((e: Monaco.editor.IEditorMouseEvent) => {
      // In comment mode, clicking on line content opens comment dialog
      const isLineClick =
        e.target.type === monaco.editor.MouseTargetType.CONTENT_TEXT ||
        e.target.type === monaco.editor.MouseTargetType.CONTENT_EMPTY;

      if (isLineClick && modeRef.current === "comment") {
        const position = e.target.position;
        if (position) {
          const model = modifiedEditor.getModel();
          const selection = modifiedEditor.getSelection();
          let selectedText = "";
          let startLine = position.lineNumber;
          let endLine = position.lineNumber;

          if (selection && !selection.isEmpty() && model) {
            selectedText = model.getValueInRange(selection);
            startLine = selection.startLineNumber;
            endLine = selection.endLineNumber;
          } else if (model) {
            selectedText = model.getLineContent(position.lineNumber) || "";
          }

          setShowCommentDialog({
            line: startLine,
            side: "right",
            selectedText,
            startLine,
            endLine,
          });
        }
      }
    });

    // Add content change listener for auto-save
    contentChangeDisposableRef.current?.dispose();
    contentChangeDisposableRef.current = modifiedEditor.onDidChangeModelContent(() => {
      scheduleSaveRef.current?.();
    });

    // Cleanup function
    return () => {
      contentChangeDisposableRef.current?.dispose();
      contentChangeDisposableRef.current = null;
      if (editorRef.current) {
        editorRef.current.dispose();
        editorRef.current = null;
      }
    };
  }, [monacoLoaded, fileDiff, isMobile]);

  const loadDiffs = async () => {
    try {
      setLoading(true);
      setError(null);
      const response = await api.getGitDiffs(cwd);
      setDiffs(response.diffs);
      setGitRoot(response.gitRoot);
      // Auto-select working changes if non-empty
      if (response.diffs.length > 0) {
        const working = response.diffs.find((d) => d.id === "working");
        if (working && working.filesCount > 0) {
          setSelectedDiff("working");
        } else if (response.diffs.length > 1) {
          setSelectedDiff(response.diffs[1].id);
        }
      }
    } catch (err) {
      const errStr = String(err);
      if (errStr.toLowerCase().includes("not a git repository")) {
        setError(`Not a git repository: ${cwd}`);
      } else {
        setError(`Failed to load diffs: ${errStr}`);
      }
    } finally {
      setLoading(false);
    }
  };

  const loadFiles = async (diffId: string) => {
    try {
      setLoading(true);
      setError(null);
      const filesData = await api.getGitDiffFiles(diffId, cwd);
      setFiles(filesData || []);
      if (filesData && filesData.length > 0) {
        setSelectedFile(filesData[0].path);
      } else {
        setSelectedFile(null);
        setFileDiff(null);
      }
    } catch (err) {
      setError(`Failed to load files: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const loadFileDiff = async (diffId: string, filePath: string) => {
    try {
      setLoading(true);
      setError(null);
      const diffData = await api.getGitFileDiff(diffId, filePath, cwd);
      setFileDiff(diffData);
    } catch (err) {
      setError(`Failed to load file diff: ${err}`);
    } finally {
      setLoading(false);
    }
  };

  const handleAddComment = () => {
    if (!showCommentDialog || !commentText.trim() || !selectedFile) return;

    // Format and append comment directly to the message input
    let commentBlock = `**${selectedFile}**`;
    if (showCommentDialog.startLine !== showCommentDialog.endLine) {
      commentBlock += ` (lines ${showCommentDialog.startLine}-${showCommentDialog.endLine})`;
    } else {
      commentBlock += ` (line ${showCommentDialog.line})`;
    }
    commentBlock += ":\n";
    if (showCommentDialog.selectedText) {
      commentBlock += "```\n" + showCommentDialog.selectedText + "\n```\n";
    }
    commentBlock += commentText + "\n\n";

    onCommentTextChange(commentBlock);
    setShowCommentDialog(null);
    setCommentText("");
  };

  const goToNextFile = useCallback(() => {
    if (files.length === 0 || !selectedFile) return false;
    const idx = files.findIndex((f) => f.path === selectedFile);
    if (idx < files.length - 1) {
      setSelectedFile(files[idx + 1].path);
      setCurrentChangeIndex(-1); // Reset to start of new file
      return true;
    }
    return false;
  }, [files, selectedFile]);

  const goToPreviousFile = useCallback(() => {
    if (files.length === 0 || !selectedFile) return false;
    const idx = files.findIndex((f) => f.path === selectedFile);
    if (idx > 0) {
      setSelectedFile(files[idx - 1].path);
      setCurrentChangeIndex(-1); // Will go to last change when file loads
      return true;
    }
    return false;
  }, [files, selectedFile]);

  const goToNextChange = useCallback(() => {
    if (!editorRef.current) return;
    const changes = editorRef.current.getLineChanges();
    if (!changes || changes.length === 0) {
      // No changes in this file, try next file
      goToNextFile();
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const nextIdx = currentChangeIndex + 1;

    if (nextIdx >= changes.length) {
      // At end of file, try to go to next file
      if (goToNextFile()) {
        return;
      }
      // No next file, stay at last change
      return;
    }

    const change = changes[nextIdx];
    const targetLine = change.modifiedStartLineNumber || 1;
    modifiedEditor.revealLineInCenter(targetLine);
    modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
    setCurrentChangeIndex(nextIdx);
  }, [currentChangeIndex, goToNextFile]);

  const goToPreviousChange = useCallback(() => {
    if (!editorRef.current) return;
    const changes = editorRef.current.getLineChanges();
    if (!changes || changes.length === 0) {
      // No changes in this file, try previous file
      goToPreviousFile();
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const prevIdx = currentChangeIndex <= 0 ? -1 : currentChangeIndex - 1;

    if (prevIdx < 0) {
      // At start of file, try to go to previous file
      if (goToPreviousFile()) {
        return;
      }
      // No previous file, go to first change
      const change = changes[0];
      const targetLine = change.modifiedStartLineNumber || 1;
      modifiedEditor.revealLineInCenter(targetLine);
      modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
      setCurrentChangeIndex(0);
      return;
    }

    const change = changes[prevIdx];
    const targetLine = change.modifiedStartLineNumber || 1;
    modifiedEditor.revealLineInCenter(targetLine);
    modifiedEditor.setPosition({ lineNumber: targetLine, column: 1 });
    setCurrentChangeIndex(prevIdx);
  }, [currentChangeIndex, goToPreviousFile]);

  // Save the current file (in edit mode)
  const saveCurrentFile = useCallback(async () => {
    if (
      !editorRef.current ||
      !selectedFile ||
      !fileDiff ||
      modeRef.current !== "edit" ||
      !gitRoot
    ) {
      return;
    }

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const model = modifiedEditor.getModel();
    if (!model) return;

    const content = model.getValue();
    const fullPath = gitRoot + "/" + selectedFile;

    try {
      setSaveStatus("saving");
      const response = await fetch("/api/write-file", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: fullPath, content }),
      });

      if (response.ok) {
        setSaveStatus("saved");
        setTimeout(() => setSaveStatus("idle"), 2000);
      } else {
        setSaveStatus("error");
        setTimeout(() => setSaveStatus("idle"), 3000);
      }
    } catch (err) {
      console.error("Failed to save:", err);
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    }
  }, [selectedFile, fileDiff, gitRoot]);

  // Debounced auto-save
  const scheduleSave = useCallback(() => {
    if (modeRef.current !== "edit") return; // Only auto-save in edit mode
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current);
    }
    pendingSaveRef.current = saveCurrentFile;
    saveTimeoutRef.current = window.setTimeout(() => {
      pendingSaveRef.current?.();
      pendingSaveRef.current = null;
      saveTimeoutRef.current = null;
    }, 1000);
  }, [saveCurrentFile]);

  // Keep scheduleSaveRef in sync
  useEffect(() => {
    scheduleSaveRef.current = scheduleSave;
  }, [scheduleSave]);

  // Force immediate save (for Ctrl+S)
  const saveImmediately = useCallback(() => {
    if (saveTimeoutRef.current) {
      clearTimeout(saveTimeoutRef.current);
      saveTimeoutRef.current = null;
    }
    pendingSaveRef.current = null;
    saveCurrentFile();
  }, [saveCurrentFile]);

  // Keyboard shortcuts
  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (showCommentDialog) {
          setShowCommentDialog(null);
        } else {
          onClose();
        }
        return;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "s") {
        e.preventDefault();
        saveImmediately();
        return;
      }
      if (!e.ctrlKey) return;
      if (e.key === "j") {
        e.preventDefault();
        goToNextFile();
      } else if (e.key === "k") {
        e.preventDefault();
        goToPreviousFile();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen, goToNextFile, goToPreviousFile, showCommentDialog, onClose, saveImmediately]);

  if (!isOpen) return null;

  const getStatusSymbol = (status: string) => {
    switch (status) {
      case "added":
        return "+";
      case "deleted":
        return "-";
      case "modified":
        return "~";
      default:
        return "";
    }
  };

  const currentFileIndex = files.findIndex((f) => f.path === selectedFile);
  const hasNextFile = currentFileIndex < files.length - 1;
  const hasPrevFile = currentFileIndex > 0;

  return (
    <div className="diff-viewer-overlay">
      <div className="diff-viewer-container">
        {/* Toast notification */}
        {saveStatus !== "idle" && (
          <div className={`diff-viewer-toast diff-viewer-toast-${saveStatus}`}>
            {saveStatus === "saving" && "💾 Saving..."}
            {saveStatus === "saved" && "✅ Saved"}
            {saveStatus === "error" && "❌ Error saving"}
          </div>
        )}

        {/* Header */}
        <div className="diff-viewer-header">
          <div className="diff-viewer-header-row">
            {/* Mode toggle */}
            <div className="diff-viewer-mode-toggle">
              <button
                className={`diff-viewer-mode-btn ${mode === "comment" ? "active" : ""}`}
                onClick={() => setMode("comment")}
                title="Comment mode"
              >
                💬
              </button>
              <button
                className={`diff-viewer-mode-btn ${mode === "edit" ? "active" : ""}`}
                onClick={() => setMode("edit")}
                title="Edit mode"
              >
                ✏️
              </button>
            </div>

            {/* Navigation buttons: <<(prev file) <(prev change) >(next change) >>(next file) */}
            <div className="diff-viewer-nav-buttons">
              <button
                className="diff-viewer-nav-btn"
                onClick={goToPreviousFile}
                disabled={!hasPrevFile}
                title="Previous file"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M11 2L5 8l6 6V2zM4 2v12H2V2h2z" />
                </svg>
              </button>
              <button
                className="diff-viewer-nav-btn"
                onClick={goToPreviousChange}
                disabled={!fileDiff}
                title="Previous change"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M10 2L4 8l6 6V2z" />
                </svg>
              </button>
              <button
                className="diff-viewer-nav-btn"
                onClick={goToNextChange}
                disabled={!fileDiff}
                title="Next change"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M6 2l6 6-6 6V2z" />
                </svg>
              </button>
              <button
                className="diff-viewer-nav-btn"
                onClick={() => goToNextFile()}
                disabled={!hasNextFile}
                title="Next file"
              >
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
                  <path d="M5 2l6 6-6 6V2zM12 2v12h2V2h-2z" />
                </svg>
              </button>
            </div>

            {/* Expand/collapse selectors */}
            <button
              className="diff-viewer-expand-btn"
              onClick={() => setSelectorsExpanded(!selectorsExpanded)}
              title={selectorsExpanded ? "Collapse selectors" : "Expand selectors"}
            >
              {selectorsExpanded ? "▲" : "▼"}
              <span className="diff-viewer-expand-label">
                {selectedFile ? selectedFile.split("/").pop() : "Select..."}
              </span>
            </button>

            <button className="diff-viewer-close" onClick={onClose} title="Close (Esc)">
              ×
            </button>
          </div>

          {/* Collapsible selectors */}
          {selectorsExpanded && (
            <div className="diff-viewer-selectors">
              {/* Diff selector */}
              <select
                value={selectedDiff || ""}
                onChange={(e) => setSelectedDiff(e.target.value || null)}
                className="diff-viewer-select"
              >
                <option value="">Choose base...</option>
                {diffs.map((diff) => {
                  const stats = `${diff.filesCount} files, +${diff.additions}/-${diff.deletions}`;
                  return (
                    <option key={diff.id} value={diff.id}>
                      {diff.id === "working"
                        ? `Working Changes (${stats})`
                        : `${diff.message.slice(0, 40)} (${stats})`}
                    </option>
                  );
                })}
              </select>

              {/* File selector */}
              <select
                value={selectedFile || ""}
                onChange={(e) => setSelectedFile(e.target.value || null)}
                className="diff-viewer-select"
                disabled={files.length === 0}
              >
                <option value="">{files.length === 0 ? "No files" : "Choose file..."}</option>
                {files.map((file) => (
                  <option key={file.path} value={file.path}>
                    {getStatusSymbol(file.status)} {file.path}
                    {file.additions > 0 && ` (+${file.additions})`}
                    {file.deletions > 0 && ` (-${file.deletions})`}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>

        {/* Error banner */}
        {error && <div className="diff-viewer-error">{error}</div>}

        {/* Main content */}
        <div className="diff-viewer-content">
          {loading && !fileDiff && (
            <div className="diff-viewer-loading">
              <div className="spinner"></div>
              <span>Loading...</span>
            </div>
          )}

          {!loading && !monacoLoaded && !error && (
            <div className="diff-viewer-loading">
              <div className="spinner"></div>
              <span>Loading editor...</span>
            </div>
          )}

          {!loading && monacoLoaded && !fileDiff && !error && (
            <div className="diff-viewer-empty">
              <p>Select a diff and file to view changes.</p>
              <p className="diff-viewer-hint">Click on line numbers to add comments.</p>
            </div>
          )}

          {/* Monaco editor container */}
          <div
            ref={editorContainerRef}
            className="diff-viewer-editor"
            style={{ display: fileDiff && monacoLoaded ? "block" : "none" }}
          />
        </div>

        {/* Comment dialog */}
        {showCommentDialog && (
          <div className="diff-viewer-comment-dialog">
            <h4>
              Add Comment (Line
              {showCommentDialog.startLine !== showCommentDialog.endLine
                ? `s ${showCommentDialog.startLine}-${showCommentDialog.endLine}`
                : ` ${showCommentDialog.line}`}
              , {showCommentDialog.side === "left" ? "old" : "new"})
            </h4>
            {showCommentDialog.selectedText && (
              <pre className="diff-viewer-selected-text">{showCommentDialog.selectedText}</pre>
            )}
            <textarea
              value={commentText}
              onChange={(e) => setCommentText(e.target.value)}
              placeholder="Enter your comment..."
              className="diff-viewer-comment-input"
              autoFocus
            />
            <div className="diff-viewer-comment-actions">
              <button
                onClick={() => setShowCommentDialog(null)}
                className="diff-viewer-btn diff-viewer-btn-secondary"
              >
                Cancel
              </button>
              <button
                onClick={handleAddComment}
                className="diff-viewer-btn diff-viewer-btn-primary"
                disabled={!commentText.trim()}
              >
                Add Comment
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export default DiffViewer;
