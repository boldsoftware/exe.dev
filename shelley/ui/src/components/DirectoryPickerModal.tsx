import React, { useState, useEffect, useRef, useCallback } from "react";
import { api } from "../services/api";

interface DirectoryEntry {
  name: string;
  is_dir: boolean;
}

interface CachedDirectory {
  path: string;
  parent: string;
  entries: DirectoryEntry[];
}

interface DirectoryPickerModalProps {
  isOpen: boolean;
  onClose: () => void;
  onSelect: (path: string) => void;
  initialPath?: string;
}

function DirectoryPickerModal({
  isOpen,
  onClose,
  onSelect,
  initialPath,
}: DirectoryPickerModalProps) {
  const [inputPath, setInputPath] = useState(() => {
    if (!initialPath) return "";
    return initialPath.endsWith("/") ? initialPath : initialPath + "/";
  });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Cache for directory listings
  const cacheRef = useRef<Map<string, CachedDirectory>>(new Map());

  // Current directory being displayed (the parent directory of what's being typed)
  const [displayDir, setDisplayDir] = useState<CachedDirectory | null>(null);
  // Filter prefix (the part after the last slash that we're filtering by)
  const [filterPrefix, setFilterPrefix] = useState("");

  // Parse input path into directory and filter prefix
  const parseInputPath = useCallback((path: string): { dirPath: string; prefix: string } => {
    if (!path) {
      return { dirPath: "", prefix: "" };
    }

    // If path ends with /, we're looking at contents of that directory
    if (path.endsWith("/")) {
      return { dirPath: path.slice(0, -1) || "/", prefix: "" };
    }

    // Otherwise, split into directory and prefix
    const lastSlash = path.lastIndexOf("/");
    if (lastSlash === -1) {
      // No slash, treat as prefix in current directory
      return { dirPath: "", prefix: path };
    }
    if (lastSlash === 0) {
      // Root directory with prefix
      return { dirPath: "/", prefix: path.slice(1) };
    }
    return {
      dirPath: path.slice(0, lastSlash),
      prefix: path.slice(lastSlash + 1),
    };
  }, []);

  // Load directory from cache or API
  const loadDirectory = useCallback(async (path: string): Promise<CachedDirectory | null> => {
    const normalizedPath = path || "/";

    // Check cache first
    const cached = cacheRef.current.get(normalizedPath);
    if (cached) {
      return cached;
    }

    // Load from API
    setLoading(true);
    setError(null);
    try {
      const result = await api.listDirectory(path || undefined);
      if (result.error) {
        setError(result.error);
        return null;
      }

      const dirData: CachedDirectory = {
        path: result.path,
        parent: result.parent,
        entries: result.entries || [],
      };

      // Cache it
      cacheRef.current.set(result.path, dirData);

      return dirData;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load directory");
      return null;
    } finally {
      setLoading(false);
    }
  }, []);

  // Update display when input changes
  useEffect(() => {
    if (!isOpen) return;

    const { dirPath, prefix } = parseInputPath(inputPath);
    setFilterPrefix(prefix);

    // Load the directory
    loadDirectory(dirPath).then((dir) => {
      if (dir) {
        setDisplayDir(dir);
        setError(null);
      }
    });
  }, [isOpen, inputPath, parseInputPath, loadDirectory]);

  // Initialize when modal opens
  useEffect(() => {
    if (isOpen) {
      if (!initialPath) {
        setInputPath("");
      } else {
        setInputPath(initialPath.endsWith("/") ? initialPath : initialPath + "/");
      }
      // Clear cache on open to get fresh data
      cacheRef.current.clear();
    }
  }, [isOpen, initialPath]);

  // Focus input when modal opens (but not on mobile to avoid keyboard popup)
  useEffect(() => {
    if (isOpen && inputRef.current) {
      // Check if mobile device (touch-based)
      const isMobile = window.matchMedia("(max-width: 768px)").matches || "ontouchstart" in window;
      if (!isMobile) {
        inputRef.current.focus();
        // Move cursor to end
        const len = inputRef.current.value.length;
        inputRef.current.setSelectionRange(len, len);
      }
    }
  }, [isOpen]);

  // Filter entries based on prefix (case-insensitive)
  const filteredEntries =
    displayDir?.entries.filter((entry) => {
      if (!filterPrefix) return true;
      return entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase());
    }) || [];

  const handleEntryClick = (entry: DirectoryEntry) => {
    if (entry.is_dir) {
      const basePath = displayDir?.path || "";
      const newPath = basePath === "/" ? `/${entry.name}/` : `${basePath}/${entry.name}/`;
      setInputPath(newPath);
    }
  };

  const handleParentClick = () => {
    if (displayDir?.parent) {
      const newPath = displayDir.parent === "/" ? "/" : `${displayDir.parent}/`;
      setInputPath(newPath);
    }
  };

  const handleInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      handleSelect();
    }
  };

  const handleSelect = () => {
    // Use the current directory path for selection
    const { dirPath } = parseInputPath(inputPath);
    const selectedPath = inputPath.endsWith("/") ? (dirPath === "/" ? "/" : dirPath) : dirPath;
    onSelect(selectedPath || displayDir?.path || "");
    onClose();
  };

  const handleBackdropClick = (e: React.MouseEvent) => {
    if (e.target === e.currentTarget) {
      onClose();
    }
  };

  if (!isOpen) return null;

  // Determine if we should show the parent entry
  const showParent = displayDir?.parent && displayDir.parent !== "";

  return (
    <div className="modal-overlay" onClick={handleBackdropClick}>
      <div className="modal directory-picker-modal">
        {/* Header */}
        <div className="modal-header">
          <h2 className="modal-title">Select Directory</h2>
          <button onClick={onClose} className="btn-icon" aria-label="Close modal">
            <svg fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M6 18L18 6M6 6l12 12"
              />
            </svg>
          </button>
        </div>

        {/* Content */}
        <div className="modal-body directory-picker-body">
          {/* Path input */}
          <div className="directory-picker-input-container">
            <input
              ref={inputRef}
              type="text"
              value={inputPath}
              onChange={(e) => setInputPath(e.target.value)}
              onKeyDown={handleInputKeyDown}
              className="directory-picker-input"
              placeholder="/path/to/directory"
            />
          </div>

          {/* Current directory indicator */}
          {displayDir && (
            <div className="directory-picker-current">
              {displayDir.path}
              {filterPrefix && <span className="directory-picker-filter">/{filterPrefix}*</span>}
            </div>
          )}

          {/* Error message */}
          {error && <div className="directory-picker-error">{error}</div>}

          {/* Loading state */}
          {loading && (
            <div className="directory-picker-loading">
              <div className="spinner spinner-small"></div>
              <span>Loading...</span>
            </div>
          )}

          {/* Directory listing */}
          {!loading && !error && (
            <div className="directory-picker-list">
              {/* Parent directory entry */}
              {showParent && (
                <button
                  className="directory-picker-entry directory-picker-entry-parent"
                  onClick={handleParentClick}
                >
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className="directory-picker-icon"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M11 17l-5-5m0 0l5-5m-5 5h12"
                    />
                  </svg>
                  <span>..</span>
                </button>
              )}

              {/* Directory entries */}
              {filteredEntries.map((entry) => (
                <button
                  key={entry.name}
                  className="directory-picker-entry"
                  onClick={() => handleEntryClick(entry)}
                >
                  <svg
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                    className="directory-picker-icon"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"
                    />
                  </svg>
                  <span>
                    {filterPrefix &&
                    entry.name.toLowerCase().startsWith(filterPrefix.toLowerCase()) ? (
                      <>
                        <strong>{entry.name.slice(0, filterPrefix.length)}</strong>
                        {entry.name.slice(filterPrefix.length)}
                      </>
                    ) : (
                      entry.name
                    )}
                  </span>
                </button>
              ))}

              {/* Empty state */}
              {filteredEntries.length === 0 && !showParent && (
                <div className="directory-picker-empty">
                  {filterPrefix ? "No matching directories" : "No subdirectories"}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="directory-picker-footer">
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn-primary" onClick={handleSelect} disabled={loading || !!error}>
            Select
          </button>
        </div>
      </div>
    </div>
  );
}

export default DirectoryPickerModal;
